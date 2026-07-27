package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envFileSample keeps the shape of the real file: comments on their own lines, a commented-out
// alternative, and a key that is present but empty.
const envFileSample = `# Deployment config
SESSION_NAME=My ARK SE Server
SERVER_MAP=TheIsland
MAX_PLAYERS=2

# Lifecycle
UPDATE_ON_START=true
BACKUP_ON_STOP=true
ENABLE_GAME_LOG=
ADMIN_PASSWORD=hunter2
#PANEL_PASS=old-one
PANEL_PASS=secret
`

func envWriteFixture(t *testing.T) config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(envFileSample), 0o600); err != nil {
		t.Fatal(err)
	}
	return config{dataDir: t.TempDir(), envDir: dir, envWrite: path}
}

func envFileText(t *testing.T, cfg config) string {
	t.Helper()
	b, err := os.ReadFile(envWritePath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSetEnvLineChangesOneLineAndKeepsTheRest(t *testing.T) {
	got := setEnvLine(envFileSample, "MAX_PLAYERS", "10")
	if !strings.Contains(got, "MAX_PLAYERS=10") {
		t.Fatal("the value was not written")
	}
	before, after := strings.Split(envFileSample, "\n"), strings.Split(got, "\n")
	if len(before) != len(after) {
		t.Fatalf("line count changed: %d -> %d", len(before), len(after))
	}
	changed := 0
	for i := range before {
		if before[i] != after[i] {
			changed++
		}
	}
	if changed != 1 {
		t.Errorf("%d lines changed, want 1", changed)
	}
	// A commented-out key is not the key. Writing PANEL_PASS must not revive the old line above it.
	if got := setEnvLine(envFileSample, "PANEL_PASS", "new"); strings.Contains(got, "#PANEL_PASS=new") {
		t.Error("a commented line was treated as the assignment")
	}
}

func TestSetEnvLineAppendsAnAbsentKey(t *testing.T) {
	got := setEnvLine("A=1\n", "WARN_ON_STOP", "true")
	if got != "A=1\nWARN_ON_STOP=true\n" {
		t.Errorf("appended badly: %q", got)
	}
}

func TestValidateEnvValueRefusesWhatWouldSmuggleALine(t *testing.T) {
	// The file is sourced by a shell, so a newline in a value would add a second assignment.
	if err := validateEnvValue("SESSION_NAME", "harmless\nADMIN_PASSWORD=owned"); err == nil {
		t.Error("a newline was accepted")
	}
	if err := validateEnvValue("SESSION_NAME", strings.Repeat("a", maxEnvValue+1)); err == nil {
		t.Error("an oversized value was accepted")
	}
	// A switch reads as off on any typo, so only the two words and empty are accepted.
	for _, bad := range []string{"yes", "1", "True "} {
		if err := validateEnvValue("BACKUP_ON_STOP", bad); err == nil {
			t.Errorf("%q was accepted for a switch", bad)
		}
	}
	for _, good := range []string{"", "true", "false", "TRUE"} {
		if err := validateEnvValue("BACKUP_ON_STOP", good); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}
	for _, bad := range []string{"0", "-1", "zwei", "2.5"} {
		if err := validateEnvValue("MAX_PLAYERS", bad); err == nil {
			t.Errorf("%q was accepted as a count", bad)
		}
	}
}

func TestApplyEnvEditsWritesOnlyWhatChanged(t *testing.T) {
	cfg := envWriteFixture(t)
	form := url.Values{
		"SESSION_NAME":    {"Neuer Name"},
		"MAX_PLAYERS":     {"2"}, // unchanged
		"BACKUP_ON_STOP":  {"false"},
		"ENABLE_GAME_LOG": {""}, // unchanged, was empty
	}
	changed, err := applyEnvEdits(cfg, form, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if changed != 2 {
		t.Errorf("changed %d, want 2", changed)
	}
	text := envFileText(t, cfg)
	if !strings.Contains(text, "SESSION_NAME=Neuer Name") || !strings.Contains(text, "BACKUP_ON_STOP=false") {
		t.Errorf("file after write:\n%s", text)
	}
	if !strings.Contains(text, "MAX_PLAYERS=2") {
		t.Error("an unchanged value was lost")
	}
	if !strings.Contains(text, "# Lifecycle") {
		t.Error("the comments were lost")
	}
}

func TestApplyEnvEditsKeepsASecretThatWasLeftEmpty(t *testing.T) {
	// The field is never rendered with its value, so an empty one is an untouched password and not an
	// instruction to clear it.
	cfg := envWriteFixture(t)
	changed, err := applyEnvEdits(cfg, url.Values{"ADMIN_PASSWORD": {""}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Errorf("changed %d, want none", changed)
	}
	if !strings.Contains(envFileText(t, cfg), "ADMIN_PASSWORD=hunter2") {
		t.Error("the password was cleared")
	}
}

func TestApplyEnvEditsRejectsTheWholeFormOnOneBadValue(t *testing.T) {
	cfg := envWriteFixture(t)
	before := envFileText(t, cfg)
	_, err := applyEnvEdits(cfg, url.Values{
		"SESSION_NAME":   {"fine"},
		"BACKUP_ON_STOP": {"maybe"},
	}, time.Now())
	if err == nil {
		t.Fatal("the form was accepted")
	}
	if envFileText(t, cfg) != before {
		t.Error("the file was changed although the form was rejected")
	}
}

func TestApplyEnvEditsKeepsACopyOfWhatItReplaced(t *testing.T) {
	cfg := envWriteFixture(t)
	if _, err := applyEnvEdits(cfg, url.Values{"SESSION_NAME": {"Zweiter Name"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.dataDir, "panel", "env-history")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("history: %v, %d entries", err, len(entries))
	}
	b, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != envFileSample {
		t.Error("the copy is not the file as it was before the edit")
	}
}

func TestApplyEnvEditsPrunesTheHistory(t *testing.T) {
	cfg := envWriteFixture(t)
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	for i := 0; i < envHistoryKeep+4; i++ {
		form := url.Values{"SESSION_NAME": {"Name " + strings.Repeat("x", i+1)}}
		if _, err := applyEnvEdits(cfg, form, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(cfg.dataDir, "panel", "env-history"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != envHistoryKeep {
		t.Errorf("%d copies kept, want %d", len(entries), envHistoryKeep)
	}
}

func TestApplyEnvEditsRefusedWhenWritingIsNotWiredUp(t *testing.T) {
	cfg := envWriteFixture(t)
	cfg.envWrite = ""
	if _, err := applyEnvEdits(cfg, url.Values{"SESSION_NAME": {"x"}}, time.Now()); err == nil {
		t.Error("wrote without the writable mount being configured")
	}
}

func TestWriteEnvInPlaceKeepsTheInode(t *testing.T) {
	// The file is a mount point in the real deployment: a rename would swap the container's own view
	// and leave the host's file untouched, so the write has to go to the same inode.
	cfg := envWriteFixture(t)
	before, err := os.Stat(envWritePath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeEnvInPlace(envWritePath(cfg), "A=1\n"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(envWritePath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("the file was replaced instead of written")
	}
}
