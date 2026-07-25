package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveTextFileKeepsTheSymlinkIntact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on Windows; the target platform is Linux")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real", "GameUserSettings.ini")
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "GameUserSettings.ini")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if err := saveTextFile(link, "new\n"); err != nil {
		t.Fatalf("save: %v", err)
	}

	// The link must still be a link, and the real file must carry the new content.
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the save replaced the symlink with a plain file")
	}
	got, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Errorf("target content is %q", got)
	}
}

func TestSaveTextFileKeepsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits do not apply on Windows")
	}
	path := filepath.Join(t.TempDir(), "Game.ini")
	if err := os.WriteFile(path, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveTextFile(path, "b\n"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("want mode 600 preserved, got %o", perm)
	}
}

func TestSaveTextFileNormalizesNewlines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Game.ini")
	if err := saveTextFile(path, "a\r\nb\r\n"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a\nb\n" {
		t.Errorf("want the carriage returns gone, got %q", got)
	}
}

func TestMaskSecrets(t *testing.T) {
	in := strings.Join([]string{
		"SESSION_NAME=My Server",
		"SERVER_PASSWORD=hunter2",
		"ADMIN_PASSWORD=letmein",
		"# ADMIN_PASSWORD=commented",
		"MAX_PLAYERS=2",
		"",
	}, "\n")
	got := maskSecrets(in)

	for _, secret := range []string{"hunter2", "letmein"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q survived masking:\n%s", secret, got)
		}
	}
	// Everything that is not a credential has to stay readable, comments included.
	for _, keep := range []string{"SESSION_NAME=My Server", "MAX_PLAYERS=2", "# ADMIN_PASSWORD=commented"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%q should have been left alone:\n%s", keep, got)
		}
	}
}

func TestTailFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := tailFile(path, 10)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 10 {
		t.Fatalf("want 10 lines, got %d", len(lines))
	}
	if lines[9] != "line 500" || lines[0] != "line 491" {
		t.Errorf("want lines 491..500, got %q..%q", lines[0], lines[9])
	}
}

// A file shorter than the read window must come back whole, with its first line intact.
func TestTailFileShorterThanWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.log")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := tailFile(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got != "first\nsecond" {
		t.Errorf("want both lines, got %q", got)
	}
}

func TestTailFileEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := tailFile(path, 10)
	if err != nil {
		t.Fatalf("an empty log is normal, not an error: %v", err)
	}
	if got != "" {
		t.Errorf("want nothing, got %q", got)
	}
}

func TestFindFileRejectsUnknownID(t *testing.T) {
	cfg := config{dataDir: "/data", envDir: "/deploy"}
	// The browser only ever sends an id, so a path can never reach the filesystem layer.
	for _, id := range []string{"../../etc/passwd", "", "unknown"} {
		if _, ok := findFile(configFiles(cfg), id); ok {
			t.Errorf("id %q must not resolve", id)
		}
	}
	if _, ok := findFile(configFiles(cfg), "game"); !ok {
		t.Error("a known id must resolve")
	}
}
