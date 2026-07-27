// Writing the deployment's .env. Off unless the deployment wires it up, and then narrow on purpose.
//
// Why it exists at all: applying one of these values needs the container recreated, and for a while
// that was reason enough to leave the file alone, since whoever stands in the deployment directory has
// it open anyway. That misses the case it was meant for. The operator can put `docker compose up -d`
// behind a shortcut on the host and never open a terminal, and then editing the value is the only part
// that still needed one.
//
// Why it is a second mount rather than the read-only one made writable: the panel has the deployment
// directory mounted whole, and writable that would include the docker-compose.yml. Whoever can write
// that file decides everything about the next `up -d`, up to mounting the host's root directory into a
// container. So the compose file stays read-only and only the .env comes in a second time, as a single
// file, writable.
//
// A single-file mount pins the inode, which rules out the atomic write-and-rename used everywhere else
// here: a rename would replace the mount point inside the container and leave the host's file
// untouched. This writes the file in place instead, and takes a copy onto the server volume first,
// because an in-place write that is interrupted leaves the file short.
package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// envWriteKind decides how a value is offered and what is accepted back. Free text for a name, a
// choice for a switch, digits for a count: a typo in a switch is silent, the server reads it as off.
type envWriteKind int

const (
	envText envWriteKind = iota
	envBool
	envNumber
)

// envKinds types the keys the form offers. Anything not named here is not editable, and the list is
// deliberately the same set the page already shows: the structural keys (the volume, the ids, the
// memory cap, the ports) belong to the host, not to the game session.
var envKinds = map[string]envWriteKind{
	"UPDATE_ON_START":   envBool,
	"BACKUP_ON_STOP":    envBool,
	"PRE_UPDATE_BACKUP": envBool,
	"WARN_ON_STOP":      envBool,
	"DISABLE_BATTLEYE":  envBool,
	"ENABLE_CROSSPLAY":  envBool,
	"ENABLE_GAME_LOG":   envBool,
	"MAX_PLAYERS":       envNumber,
}

func envKindOf(key string) envWriteKind {
	if k, ok := envKinds[key]; ok {
		return k
	}
	return envText
}

// maxEnvValue caps a single value. These are names, ids and switches; anything far past this is a
// mistake or an attempt.
const maxEnvValue = 512

// envHistoryKeep is how many copies of the previous file are kept. Enough to walk back from a bad
// edit, few enough that nobody has to clean up after it.
const envHistoryKeep = 10

func envWritePath(cfg config) string { return cfg.envWrite }

func envWritable(cfg config) bool { return cfg.envWrite != "" }

// validateEnvValue is the gate. A value ends up on a line of a file that a shell sources, so a
// newline would smuggle in a second assignment, and that is refused rather than escaped.
func validateEnvValue(key, value string) error {
	switch {
	case len(value) > maxEnvValue:
		return fmt.Errorf("Wert länger als %d Zeichen", maxEnvValue)
	case strings.ContainsAny(value, "\n\r"):
		return fmt.Errorf("Zeilenumbruch im Wert")
	}
	switch envKindOf(key) {
	case envBool:
		switch strings.ToLower(value) {
		case "", "true", "false":
		default:
			return fmt.Errorf("nur true, false oder leer")
		}
	case envNumber:
		if value == "" {
			return nil
		}
		if n, err := strconv.Atoi(value); err != nil || n < 1 {
			return fmt.Errorf("ganze Zahl ab 1 erwartet")
		}
	}
	return nil
}

// setEnvLine replaces the value of one key and leaves everything else, comments included, exactly as
// it was. A key that is not in the file yet is appended, because a deployment that never set it still
// needs to be able to.
func setEnvLine(text, key, value string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		name, _, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(strings.TrimSpace(line), "#") || strings.TrimSpace(name) != key {
			continue
		}
		lines[i] = key + "=" + value
		return strings.Join(lines, "\n")
	}
	// Keep the file ending in a newline, and do not glue the new key onto the last line.
	trimmed := strings.TrimRight(text, "\n")
	return trimmed + "\n" + key + "=" + value + "\n"
}

// envEdit is one accepted change, kept apart from the writing so a rejected form changes nothing.
type envEdit struct {
	Key   string
	Value string
}

// collectEnvEdits reads the form against the current file and returns only what actually differs.
// A secret submitted empty means "leave as it is": the field is never rendered with its value, so an
// empty one is the normal state of an untouched password rather than an instruction to clear it.
func collectEnvEdits(current string, form url.Values, values []envValue) ([]envEdit, error) {
	file := parseINI(current)
	var edits []envEdit
	for _, v := range values {
		submitted, present := form[v.Key]
		if !present {
			continue
		}
		value := strings.TrimSpace(submitted[0])
		if v.Secret && value == "" {
			continue
		}
		if err := validateEnvValue(v.Key, value); err != nil {
			return nil, fmt.Errorf("%s: %w", v.Label, err)
		}
		existing, _ := file.lookup("", v.Key)
		if strings.TrimSpace(existing) == value {
			continue
		}
		edits = append(edits, envEdit{Key: v.Key, Value: value})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].Key < edits[j].Key })
	return edits, nil
}

// saveEnvCopy puts the file as it stands onto the server volume before it is changed, and prunes the
// oldest copies. The volume is the only writable place that survives the container, and the
// deployment directory is not it: everything there except this one file is mounted read-only.
func saveEnvCopy(cfg config, content string, now time.Time) error {
	dir := filepath.Join(cfg.dataDir, "panel", "env-history")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := "env-" + now.UTC().Format("2006-01-02_15.04.05") + ".bak"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		return err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // the copy is written; failing to prune is not worth losing the edit over
	}
	var copies []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".bak") {
			copies = append(copies, e.Name())
		}
	}
	sort.Strings(copies) // the stamp sorts chronologically
	for len(copies) > envHistoryKeep {
		os.Remove(filepath.Join(dir, copies[0]))
		copies = copies[1:]
	}
	return nil
}

// writeEnvInPlace writes to the same inode: truncate, write, flush. No rename, because the file is a
// mount point and renaming over it would swap the container's own view and leave the host's file as it
// was. Flushed before returning, so the answer to the browser is not ahead of the disk.
func writeEnvInPlace(path, content string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// applyEnvEdits is the whole write: read, decide, copy, write. It returns how many values changed, so
// a form that changed nothing can say so instead of claiming a save.
func applyEnvEdits(cfg config, form url.Values, now time.Time) (int, error) {
	if !envWritable(cfg) {
		return 0, fmt.Errorf("Schreiben ist in diesem Deployment nicht eingerichtet")
	}
	// Read through the writable path: it is the same file, and reading the copy that is about to be
	// written keeps a stale directory listing from being the basis of the edit.
	current, err := readTextFile(envWritePath(cfg))
	if err != nil {
		return 0, fmt.Errorf(".env nicht lesbar: %w", err)
	}
	values, err := loadEnvValues(cfg)
	if err != nil {
		return 0, err
	}

	edits, err := collectEnvEdits(current, form, values)
	if err != nil {
		return 0, err
	}
	if len(edits) == 0 {
		return 0, nil
	}

	if err := saveEnvCopy(cfg, current, now); err != nil {
		return 0, fmt.Errorf("Sicherheitskopie: %w", err)
	}
	updated := current
	for _, e := range edits {
		updated = setEnvLine(updated, e.Key, e.Value)
	}
	if err := writeEnvInPlace(envWritePath(cfg), updated); err != nil {
		return 0, fmt.Errorf(".env schreiben: %w", err)
	}
	return len(edits), nil
}

// envKindName is the kind as the template asks for it, so the control on the page and the value the
// writer accepts are decided in one place.
func envKindName(key string) string {
	switch envKindOf(key) {
	case envBool:
		return "bool"
	case envNumber:
		return "number"
	default:
		return "text"
	}
}
