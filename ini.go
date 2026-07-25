// Reading and writing the game's INI files without disturbing anything the panel does not own.
// These files carry far more than a settings form knows about: stock content overrides, keys that
// legitimately appear many times, sections nobody edits, comments. So the parser keeps every source
// line as it found it, and a write only ever touches the single line belonging to the key it was
// given. Rendering a file that was not modified returns the original bytes.
package main

import (
	"fmt"
	"slices"
	"strings"
)

type iniKey struct{ section, key string }

type iniFile struct {
	lines []string
	// where lists every line a key sits on, in file order. Repeated keys are normal in ARK's own
	// files (ConfigOverrideSupplyCrateItems appears a dozen times), so this is a slice rather than a
	// single index, and the count is what tells the caller to keep its hands off.
	where map[iniKey][]int
	// sectionOf[i] is the section line i belongs to, so a new key lands inside its block instead of
	// at the end of the file.
	sectionOf []string
}

func normKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// parseINI never fails. Anything it does not recognise is simply a line it will not touch, which is
// exactly the behaviour the panel wants for a file the game writes.
func parseINI(text string) *iniFile {
	f := &iniFile{lines: strings.Split(text, "\n"), where: map[iniKey][]int{}}
	f.sectionOf = make([]string, len(f.lines))
	section := ""
	for i, line := range f.lines {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]"):
			section = t[1 : len(t)-1]
		case t == "" || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#"):
		default:
			if k, _, ok := strings.Cut(line, "="); ok {
				id := iniKey{normKey(section), normKey(k)}
				f.where[id] = append(f.where[id], i)
			}
		}
		f.sectionOf[i] = section
	}
	return f
}

func (f *iniFile) render() string { return strings.Join(f.lines, "\n") }

// reindex rebuilds the line map after a structural change. The files run to a few hundred lines, so
// re-reading is cheaper than keeping every index correct by hand.
func (f *iniFile) reindex() { *f = *parseINI(f.render()) }

// lookup returns the value of the first occurrence and how often the key occurs at all. A count
// above one means the key is beyond the form's reach: the panel cannot know which line the operator
// meant, so it refuses rather than guesses.
func (f *iniFile) lookup(section, key string) (string, int) {
	at := f.where[iniKey{normKey(section), normKey(key)}]
	if len(at) == 0 {
		return "", 0
	}
	_, value, _ := strings.Cut(f.lines[at[0]], "=")
	return strings.TrimSpace(value), len(at)
}

func (f *iniFile) set(section, key, value string) error {
	at := f.where[iniKey{normKey(section), normKey(key)}]
	switch len(at) {
	case 0:
		f.insert(section, key, value)
	case 1:
		// Keep the spelling the file already uses for the key, and replace only what follows the
		// first equals sign: a value may itself contain one.
		name, _, _ := strings.Cut(f.lines[at[0]], "=")
		f.lines[at[0]] = name + "=" + value
	default:
		return fmt.Errorf("%s steht %d mal in [%s] und gehoert deshalb in den Roh-Editor", key, len(at), section)
	}
	f.ensureFinalNewline()
	f.reindex()
	return nil
}

// unset removes the key entirely. A value the operator never chose has no business sitting in the
// file: that is what keeps a fresh Game.ini readable, and what makes visible which settings were
// really changed.
func (f *iniFile) unset(section, key string) error {
	at := f.where[iniKey{normKey(section), normKey(key)}]
	switch len(at) {
	case 0:
		return nil
	case 1:
		f.lines = slices.Delete(f.lines, at[0], at[0]+1)
		f.reindex()
		return nil
	default:
		return fmt.Errorf("%s steht %d mal in [%s] und gehoert deshalb in den Roh-Editor", key, len(at), section)
	}
}

func (f *iniFile) insert(section, key, value string) {
	at := -1
	for i := range f.lines {
		if strings.EqualFold(f.sectionOf[i], section) {
			at = i
		}
	}
	if at < 0 {
		f.appendSection(section, key, value)
		return
	}
	// Step back over blank lines at the end of the block, so the new key joins its section instead
	// of drifting into the gap before the next one.
	for at > 0 && strings.TrimSpace(f.lines[at]) == "" {
		at--
	}
	f.lines = slices.Insert(f.lines, at+1, key+"="+value)
}

// appendSection covers the empty Game.ini: without any operator override the game leaves the file
// blank, so the first value written has to bring its section header along.
func (f *iniFile) appendSection(section, key, value string) {
	lines := f.lines
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	f.lines = append(lines, "["+section+"]", key+"="+value)
}

// ensureFinalNewline applies only to a file the panel has just changed. An untouched file is
// rendered back exactly as it came in, trailing newline or not.
func (f *iniFile) ensureFinalNewline() {
	if n := len(f.lines); n == 0 || f.lines[n-1] != "" {
		f.lines = append(f.lines, "")
	}
}
