package main

import (
	"archive/tar"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeArchive builds a plain .tar the way arkmanager lays one out: a single directory named after
// the timestamp, and the files flat inside it. Uncompressed on purpose, because the standard library
// can read bzip2 but not write it, and the restore path treats both the same up to the reader.
func writeArchive(t *testing.T, path string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	for name, content := range files {
		h := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(name, "/") {
			h.Typeflag, h.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestListBackupsFindsArchivesNewestFirst(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	root := backupDir(cfg)
	older := filepath.Join(root, "2026-07-25", "main.2026-07-25_12.00.01.tar.bz2")
	newer := filepath.Join(root, "2026-07-26", "main.2026-07-26_11.14.12.tar.bz2")
	for _, p := range []string{older, newer} {
		writeArchive(t, p, map[string]string{"stamp/TheIsland.ark": "x"})
	}
	// A file that is not an archive shares the directory and must not show up.
	if err := os.WriteFile(filepath.Join(root, "2026-07-26", "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(older, old, old); err != nil {
		t.Fatal(err)
	}

	got, err := listBackups(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Name != "main.2026-07-26_11.14.12.tar.bz2" {
		t.Errorf("newest first is %q", got[0].Name)
	}
	if got[0].ID != "2026-07-26/main.2026-07-26_11.14.12.tar.bz2" {
		t.Errorf("id is %q, want the path relative to the backup directory with forward slashes", got[0].ID)
	}
}

// A deployment whose backup cron has never run has no directory yet, and that is a normal state
// rather than something to report as broken.
func TestListBackupsWithoutDirectory(t *testing.T) {
	got, err := listBackups(config{dataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("missing backup directory should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries", len(got))
	}
}

// The id from the browser is matched against the listing rather than joined onto a path, so
// anything not in the listing simply does not resolve.
func TestFindBackupRejectsAnythingNotListed(t *testing.T) {
	entries := []backupEntry{{ID: "2026-07-26/main.tar.bz2"}}
	for _, id := range []string{"../../etc/passwd", "/etc/passwd", "2026-07-26/other.tar.bz2", ""} {
		if _, ok := findBackup(entries, id); ok {
			t.Errorf("id %q resolved", id)
		}
	}
	if _, ok := findBackup(entries, "2026-07-26/main.tar.bz2"); !ok {
		t.Error("the listed id did not resolve")
	}
}

func TestRestoreTargetDirRoutesByName(t *testing.T) {
	cfg := config{dataDir: "/data"}
	saved := filepath.Join("/data", "server", "ShooterGame", "Saved")
	cases := map[string]string{
		"TheIsland.ark":                filepath.Join(saved, "SavedArks"),
		"76500000000000000.arkprofile": filepath.Join(saved, "SavedArks"),
		"1234.arktribe":                filepath.Join(saved, "SavedArks"),
		"Game.ini":                     filepath.Join(saved, "Config", "LinuxServer"),
		"GameUserSettings.ini":         filepath.Join(saved, "Config", "LinuxServer"),
	}
	for name, want := range cases {
		if got := restoreTargetDir(cfg, savedArksDir, name); got != want {
			t.Errorf("%s -> %s, want %s", name, got, want)
		}
	}
}

func TestRestoreBackupPutsEveryFileWhereTheServerReadsIt(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	saved := filepath.Join(cfg.dataDir, "server", "ShooterGame", "Saved")
	arks, conf := filepath.Join(saved, "SavedArks"), filepath.Join(saved, "Config", "LinuxServer")
	for _, d := range []string{arks, conf} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The live state the restore overwrites, plus one of arkmanager's dated rotation copies, which
	// no archive contains and which must survive untouched.
	if err := os.WriteFile(filepath.Join(arks, "TheIsland.ark"), []byte("live world"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotation := filepath.Join(arks, "TheIsland_25.07.2026_10.42.39.ark")
	if err := os.WriteFile(rotation, []byte("rotation"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf, "Game.ini"), []byte("live ini"), 0o600); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(backupDir(cfg), "2026-07-26", "main.2026-07-26_11.14.12.tar")
	writeArchive(t, archive, map[string]string{
		"2026-07-26_11.14.12/":                     "",
		"2026-07-26_11.14.12/TheIsland.ark":        "saved world",
		"2026-07-26_11.14.12/Game.ini":             "saved ini",
		"2026-07-26_11.14.12/GameUserSettings.ini": "saved gus",
	})
	entries, err := listBackups(cfg)
	if err != nil || len(entries) != 1 {
		t.Fatalf("listing: %v, %d entries", err, len(entries))
	}

	restored, err := restoreBackup(cfg, entries[0], savedArksDir)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	// The bare directory entry carries no content and is not one of them.
	if restored != 3 {
		t.Errorf("restored %d files, want 3", restored)
	}

	for path, want := range map[string]string{
		filepath.Join(arks, "TheIsland.ark"):        "saved world",
		filepath.Join(conf, "Game.ini"):             "saved ini",
		filepath.Join(conf, "GameUserSettings.ini"): "saved gus",
		rotation: "rotation",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s holds %q, want %q", path, got, want)
		}
	}
}

// A member that names its way upwards keeps only its base name, so it lands in the save directory
// like everything else instead of somewhere on the volume.
func TestRestoreBackupKeepsMembersInsideTheTargetDirectory(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	arks := filepath.Join(cfg.dataDir, "server", "ShooterGame", "Saved", "SavedArks")
	if err := os.MkdirAll(arks, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(cfg.dataDir, "crontab")
	if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(backupDir(cfg), "2026-07-26", "main.tar")
	writeArchive(t, archive, map[string]string{"stamp/../../crontab": "evil"})
	entries, err := listBackups(cfg)
	if err != nil || len(entries) != 1 {
		t.Fatalf("listing: %v, %d entries", err, len(entries))
	}
	if _, err := restoreBackup(cfg, entries[0], savedArksDir); err != nil {
		t.Fatalf("restore: %v", err)
	}

	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "untouched" {
		t.Errorf("the archive wrote outside the target directory: %q", got)
	}
	if _, err := os.Stat(filepath.Join(arks, "crontab")); err != nil {
		t.Errorf("the member should have landed in the save directory: %v", err)
	}
}

// The server's own files are mode 600 and a restore must not loosen that, whatever the archive
// happens to say. The archive above stores 644 on purpose.
func TestRestoreBackupWritesTightPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits do not apply on Windows")
	}
	cfg := config{dataDir: t.TempDir()}
	arks := filepath.Join(cfg.dataDir, "server", "ShooterGame", "Saved", "SavedArks")
	if err := os.MkdirAll(arks, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(backupDir(cfg), "2026-07-26", "main.tar")
	writeArchive(t, archive, map[string]string{"stamp/TheIsland.ark": "world"})
	entries, err := listBackups(cfg)
	if err != nil || len(entries) != 1 {
		t.Fatalf("listing: %v, %d entries", err, len(entries))
	}
	if _, err := restoreBackup(cfg, entries[0], savedArksDir); err != nil {
		t.Fatalf("restore: %v", err)
	}

	info, err := os.Stat(filepath.Join(arks, "TheIsland.ark"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode is %o, want 600", perm)
	}
}

func TestRestoreTargetDirFollowsTheSaveDirectoryInForce(t *testing.T) {
	cfg := config{dataDir: "/data"}
	saved := filepath.Join("/data", "server", "ShooterGame", "Saved")
	// With an alternate save directory configured, the default one is not what the server reads.
	// Restoring a world there would look like it worked and change nothing about the save game being
	// played.
	if got, want := restoreTargetDir(cfg, "zweite", "TheIsland.ark"), filepath.Join(saved, "zweite"); got != want {
		t.Errorf("world -> %s, want %s", got, want)
	}
	// The INIs are the server's own and stay where they are, whatever save game is loaded.
	if got, want := restoreTargetDir(cfg, "zweite", "Game.ini"), filepath.Join(saved, "Config", "LinuxServer"); got != want {
		t.Errorf("ini -> %s, want %s", got, want)
	}
}

func TestArchiveWorldNamesTheMapAnArchiveBelongsTo(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	archive := filepath.Join(backupDir(cfg), "2026-07-26", "main.tar")
	writeArchive(t, archive, map[string]string{
		"stamp/76500000000000000.arkprofile": "profile",
		"stamp/TheCenter.ark":                "world",
		"stamp/Game.ini":                     "ini",
	})
	entries, err := listBackups(cfg)
	if err != nil || len(entries) != 1 {
		t.Fatalf("listing: %v, %d entries", err, len(entries))
	}
	world, err := archiveWorld(entries[0], entries[0].path(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if world != "TheCenter.ark" {
		t.Errorf("world %q", world)
	}
}

func TestArchiveWorldIsEmptyWhenTheArchiveCarriesNoWorld(t *testing.T) {
	// arkmanager writes exactly this when its configuration names a map whose world does not exist:
	// profile and INIs are archived, the world is missing, and the backup still reports success.
	cfg := config{dataDir: t.TempDir()}
	archive := filepath.Join(backupDir(cfg), "2026-07-26", "main.tar")
	writeArchive(t, archive, map[string]string{
		"stamp/76500000000000000.arkprofile": "profile",
		"stamp/Game.ini":                     "ini",
	})
	entries, err := listBackups(cfg)
	if err != nil || len(entries) != 1 {
		t.Fatalf("listing: %v, %d entries", err, len(entries))
	}
	world, err := archiveWorld(entries[0], entries[0].path(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if world != "" {
		t.Errorf("world %q, want none", world)
	}
}
