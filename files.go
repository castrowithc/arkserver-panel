// File access for the config editor, the .env view and the log viewer. Everything the panel may
// touch is named here; the browser only ever sends an id, never a path, so there is no traversal to
// defend against in the first place.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type fileEntry struct {
	ID       string
	Label    string
	Path     string
	Editable bool
	// Secret marks content whose values are masked before rendering.
	Secret bool
	// Exists is filled in when the list is built, so the picker can show a file that is not there
	// instead of silently dropping it.
	Exists bool
}

// configFiles are the two the server actually uses at runtime. The three list .txt files from the
// reference deployment are deliberately absent: they do not exist here, and inventing a path for a
// file nobody has seen would be a guess baked into the UI.
func configFiles(cfg config) []fileEntry {
	return []fileEntry{
		{ID: "gameusersettings", Label: "GameUserSettings.ini", Path: filepath.Join(cfg.dataDir, "GameUserSettings.ini"), Editable: true},
		{ID: "game", Label: "Game.ini", Path: filepath.Join(cfg.dataDir, "Game.ini"), Editable: true},
		{ID: "env", Label: ".env (Server)", Path: filepath.Join(cfg.envDir, ".env"), Secret: true},
	}
}

func logFiles(cfg config) []fileEntry {
	return []fileEntry{
		{ID: "arkmanager", Label: "arkmanager.log", Path: filepath.Join(cfg.dataDir, "log", "arkmanager.log")},
		{ID: "crontab", Label: "crontab.log", Path: filepath.Join(cfg.dataDir, "log", "crontab.log")},
		{ID: "gamelog", Label: "ShooterGame.log", Path: filepath.Join(cfg.dataDir, "server", "ShooterGame", "Saved", "Logs", "ShooterGame.log")},
	}
}

func findFile(entries []fileEntry, id string) (fileEntry, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	return fileEntry{}, false
}

// maxEditSize caps what the editor loads. The INIs run to a few tens of kilobytes; anything far
// past that is not something a textarea should be handed.
const maxEditSize = 1 << 20

func readTextFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxEditSize))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// saveTextFile writes atomically, but resolves the symlink first. Both INIs are symlinks into the
// server's config directory, and renaming a temp file over the link would replace the link itself
// with a plain file, quietly detaching it from the file the server reads.
func saveTextFile(path, content string) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}

	// Keep the existing permissions. The config files are mode 600 and owned by the server user,
	// and a save must not loosen that.
	mode := os.FileMode(0o600)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".panel-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file next to %s: %w", target, err)
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeded

	if _, err := tmp.WriteString(normalizeNewlines(content)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	// Flush to disk before swapping it in: a crash between rename and flush would otherwise leave
	// the server with a truncated config.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// normalizeNewlines strips the carriage returns a browser textarea submits, so the file keeps the
// line endings the server side expects.
func normalizeNewlines(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

// maskSecrets blanks the values of key=value lines whose key looks like a credential. The panel is
// behind Basic Auth on a trusted network and the operator owns these passwords anyway, so this
// guards against the incidental exposure of a shared screen or a screenshot, nothing more.
func maskSecrets(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		key, _, found := strings.Cut(line, "=")
		if !found || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if isSecretKey(key) {
			lines[i] = key + "=********"
		}
	}
	return strings.Join(lines, "\n")
}

func isSecretKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, needle := range []string{"PASSWORD", "SECRET", "TOKEN", "PASS"} {
		if strings.Contains(upper, needle) {
			return true
		}
	}
	return false
}

// tailFile returns the last n lines. It reads only the tail of the file, so a log that has grown to
// hundreds of megabytes costs the same as a small one.
func tailFile(path string, n int) (string, error) {
	const window = 64 << 10

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()
	start := size - window
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	// A window that did not reach the start of the file almost certainly cuts the first line in
	// half, so drop it rather than show a fragment.
	if start > 0 && len(lines) > 1 {
		lines = lines[1:]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}
