// The generated settings form: what the catalogue and the current files together make of a page,
// and what a submitted form makes of the files. The rule throughout is that the file stays the
// source of truth. The form shows what is in it, writes only what the operator actually changed,
// and leaves a key it cannot represent alone instead of guessing.
package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// iniSource pairs a config file with its parsed contents, and remembers a read that failed so the
// page can say so per file instead of failing as a whole.
type iniSource struct {
	entry fileEntry
	ini   *iniFile
	err   error
	dirty bool
}

func loadINISources(cfg config) map[string]*iniSource {
	out := map[string]*iniSource{}
	for _, e := range configFiles(cfg) {
		if !e.Editable {
			continue // the .env is not an INI and not ours to write
		}
		src := &iniSource{entry: e}
		content, err := readTextFile(e.Path)
		switch {
		case err == nil:
			src.ini = parseINI(content)
		case os.IsNotExist(err):
			// Until the first override the game leaves the Game.ini absent or empty. That is a
			// normal state, not a fault: the first value written creates what it needs.
			src.ini = parseINI("")
		default:
			src.err = err
		}
		out[e.ID] = src
	}
	return out
}

// settingRow is one field as the page shows it: the catalogue entry, what the file currently says,
// and why it may not be editable.
type settingRow struct {
	settingField
	Value string
	Set   bool
	Kind  string // toggle, number or text
	Hint  string
	// Locked carries the reason the field is read-only, empty when it is editable.
	Locked string
	Error  string
}

type settingGroup struct {
	Name   string
	Anchor string
	// Note is a reservation that belongs to the block as a whole rather than to any one field.
	Note string
	Rows []settingRow
}

// statArrayNote hangs on the three multiplier blocks. Their field set and index mapping come from
// the reference screens, their key names do not: those come from knowledge of the game, and this
// deployment cannot attest them either, because ARK leaves the Game.ini empty until an override
// exists and ignores a key it does not know without a word. So the block says so, rather than let a
// value that does nothing pass for a setting that was made.
const statArrayNote = "Die Key-Namen dieses Blocks sind an diesem Server nicht belegt: ein Wert " +
	"wirkt nur, wenn der Key stimmt, und trifft er nicht zu, ignoriert ARK ihn stillschweigend. " +
	"Der Faktor gilt auf den Zuwachs pro Stufe (1 = 5 pro Stufe, 4 = 20)."

var groupNotes = map[string]string{
	"Multiplikatoren Spieler":        statArrayNote,
	"Multiplikatoren wilde Dinos":    statArrayNote,
	"Multiplikatoren gezähmte Dinos": statArrayNote,
}

func (f settingField) kind() string {
	switch f.Type {
	case "checkbox", "select":
		return "toggle"
	case "number":
		return "number"
	default:
		return "text"
	}
}

func num(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// hint tells the operator the bounds the save will enforce, in the words of the catalogue.
func (f settingField) hint() string {
	if f.kind() != "number" {
		return ""
	}
	var parts []string
	switch {
	case f.Min != nil && f.Max != nil:
		parts = append(parts, num(*f.Min)+" bis "+num(*f.Max))
	case f.Min != nil:
		parts = append(parts, "ab "+num(*f.Min))
	case f.Max != nil:
		parts = append(parts, "bis "+num(*f.Max))
	}
	if f.Step != nil {
		parts = append(parts, "Schritt "+num(*f.Step))
	}
	return strings.Join(parts, ", ")
}

// toggleValue maps what the file says onto the three states the form offers. Anything that is
// neither of the two truth values stays unrecognised, and the field is shown read-only rather than
// silently turned into one of them.
func toggleValue(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1":
		return "true", true
	case "false", "0":
		return "false", true
	}
	return raw, false
}

func buildGroups(sources map[string]*iniSource) []settingGroup {
	var groups []settingGroup
	at := map[string]int{}
	for _, f := range settingFields {
		row := settingRow{settingField: f, Kind: f.kind(), Hint: f.hint()}
		src := sources[f.File]
		switch {
		case src == nil || src.err != nil:
			row.Locked = "Datei nicht lesbar"
		default:
			value, occurrences := src.ini.lookup(f.Section, f.Key)
			switch {
			case occurrences > 1:
				row.Locked = fmt.Sprintf("steht %d mal in der Datei, im Roh-Editor zu klaeren", occurrences)
			case occurrences == 1:
				row.Value, row.Set = value, true
			}
			if row.Set && row.Kind == "toggle" {
				v, ok := toggleValue(row.Value)
				row.Value = v
				if !ok {
					row.Locked = "Wert ist kein Wahrheitswert, im Roh-Editor zu klaeren"
				}
			}
		}

		i, ok := at[f.Group]
		if !ok {
			i = len(groups)
			at[f.Group] = i
			groups = append(groups, settingGroup{Name: f.Group, Anchor: anchor(f.Group), Note: groupNotes[f.Group]})
		}
		groups[i].Rows = append(groups[i].Rows, row)
	}
	return groups
}

func anchor(name string) string {
	repl := strings.NewReplacer(" ", "-", "ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss")
	return repl.Replace(strings.ToLower(name))
}

// applyForm writes the submitted values into the parsed files and reports what changed. Nothing is
// written unless every field passes: a page of settings is one operation to the operator, and a
// half-applied form would leave the server in a state nobody chose.
func applyForm(sources map[string]*iniSource, form url.Values, groups []settingGroup) (int, bool) {
	type pending struct {
		field  settingField
		value  string
		remove bool
	}
	var todo []pending
	failed := false

	for gi := range groups {
		for ri := range groups[gi].Rows {
			row := &groups[gi].Rows[ri]
			if row.Locked != "" {
				continue
			}
			submitted, present := form[row.ID]
			if !present {
				continue
			}
			raw := strings.TrimSpace(submitted[0])
			if raw == "" {
				if row.Set {
					todo = append(todo, pending{field: row.settingField, remove: true})
					row.Value, row.Set = "", false
				}
				continue
			}
			value, err := row.settingField.normalize(raw)
			if err != nil {
				row.Error, row.Value, row.Set = err.Error(), raw, true
				failed = true
				continue
			}
			// Compare against the file as it reads now, so re-submitting an unchanged form writes
			// nothing and the "edited, not yet restarted" reminder stays honest.
			current := row.Value
			if row.Kind == "toggle" {
				current, _ = toggleValue(current)
				normalized, _ := toggleValue(value)
				if row.Set && current == normalized {
					continue
				}
			} else if row.Set && current == value {
				continue
			}
			todo = append(todo, pending{field: row.settingField, value: value})
			row.Value, row.Set = value, true
			if row.Kind == "toggle" {
				row.Value, _ = toggleValue(value)
			}
		}
	}
	if failed {
		return 0, false
	}

	for _, p := range todo {
		src := sources[p.field.File]
		var err error
		if p.remove {
			err = src.ini.unset(p.field.Section, p.field.Key)
		} else {
			err = src.ini.set(p.field.Section, p.field.Key, p.value)
		}
		if err != nil {
			// The writer only refuses what the page already marks as locked, so reaching this means
			// the file changed underneath us. Stopping here is the safe end.
			return 0, false
		}
		src.dirty = true
	}
	return len(todo), true
}
