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
	if got := entries[0].World; got != "TheCenter.ark" {
		t.Errorf("world %q", got)
	}
	// The map is what the listing groups on, so it has to come out of the file name rather than
	// being carried around as the whole file name.
	if got := entries[0].Map; got != "TheCenter" {
		t.Errorf("map %q", got)
	}
	if !entries[0].HasWorld() {
		t.Error("archive with a world reports none")
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
	if entries[0].HasWorld() {
		t.Errorf("world %q, want none", entries[0].World)
	}
}

func TestSaveGameComesFromTheSidecarAndIsUnknownWithoutIt(t *testing.T) {
	cfg := config{dataDir: t.TempDir(), archives: &archiveCache{}}
	plain := filepath.Join(backupDir(cfg), "2026-07-26", "main.2026-07-26_10.00.00.tar")
	stamped := filepath.Join(backupDir(cfg), "2026-07-26", "main.2026-07-26_11.00.00.tar")
	writeArchive(t, plain, map[string]string{"stamp/TheIsland.ark": "world"})
	writeArchive(t, stamped, map[string]string{"stamp/TheIsland.ark": "world"})
	// The server writes this next to the archive. Anything it does not recognise is ignored rather
	// than treated as the value, and a trailing comment style is not invented here.
	if err := os.WriteFile(stamped+savegameStampSuffix, []byte("map=TheIsland\nsavedir=zweite-runde\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := listBackups(cfg)
	if err != nil || len(entries) != 2 {
		t.Fatalf("listing: %v, %d entries", err, len(entries))
	}
	byName := map[string]backupEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if got := byName["main.2026-07-26_11.00.00.tar"]; got.SaveGame != "zweite-runde" || !got.Stamped() {
		t.Errorf("stamped archive says %q, stamped=%v", got.SaveGame, got.Stamped())
	}
	// An archive from before the stamp existed must read as unknown. Defaulting it to SavedArks
	// would be a guess, and the restore refuses on exactly this value.
	if got := byName["main.2026-07-26_10.00.00.tar"]; got.SaveGame != "" || got.Stamped() {
		t.Errorf("unstamped archive says %q, stamped=%v", got.SaveGame, got.Stamped())
	}
}

func TestRestoreConflictRefusesAForeignMapAndAForeignSaveGame(t *testing.T) {
	island := archiveInfo{World: "TheIsland.ark", Map: "TheIsland"}
	cases := []struct {
		name     string
		entry    backupEntry
		saveDir  string
		active   string
		conflict bool
	}{
		{"same map, same save game", backupEntry{archiveInfo: with(island, "SavedArks")}, "SavedArks", "TheIsland", false},
		{"other map", backupEntry{archiveInfo: archiveInfo{World: "TheCenter.ark", Map: "TheCenter"}}, "SavedArks", "TheIsland", true},
		// The case the stamp exists for: same map, different save game. Nothing inside the archive
		// tells these apart, which is why this used to go through.
		{"same map, other save game", backupEntry{archiveInfo: with(island, "zweite-runde")}, "SavedArks", "TheIsland", true},
		// Unstamped archives predate the stamp and stay usable.
		{"unstamped", backupEntry{archiveInfo: island}, "SavedArks", "TheIsland", false},
		// An archive without a world carries no map to disagree with.
		{"no world", backupEntry{archiveInfo: with(archiveInfo{}, "SavedArks")}, "SavedArks", "TheIsland", false},
		// Without a resolvable map the map check is skipped rather than guessed.
		{"unknown active map", backupEntry{archiveInfo: archiveInfo{World: "TheCenter.ark", Map: "TheCenter"}}, "SavedArks", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := restoreConflict(c.entry, c.saveDir, c.active)
			if got := err != nil; got != c.conflict {
				t.Errorf("conflict=%v, want %v (%v)", got, c.conflict, err)
			}
		})
	}
}

func with(i archiveInfo, saveGame string) archiveInfo {
	i.SaveGame = saveGame
	return i
}

func TestStampIsPickedUpWhenItArrivesAfterTheArchive(t *testing.T) {
	// The server writes the archive first and stamps it in the step right after. A listing that
	// falls into that gap must not remember "unknown" for the rest of the panel's life, which is
	// what caching the stamp alongside the archive would do.
	cfg := config{dataDir: t.TempDir(), archives: &archiveCache{}}
	archive := filepath.Join(backupDir(cfg), "2026-07-26", "main.tar")
	writeArchive(t, archive, map[string]string{"stamp/TheIsland.ark": "world"})

	if entries, err := listBackups(cfg); err != nil || entries[0].Stamped() {
		t.Fatalf("first listing: %v, stamped=%v", err, entries[0].Stamped())
	}
	if err := os.WriteFile(archive+savegameStampSuffix, []byte("savedir=SavedArks\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := listBackups(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].SaveGame != "SavedArks" {
		t.Errorf("save game %q after the stamp arrived, want SavedArks", entries[0].SaveGame)
	}
}

func TestGroupBackupsKeepsOrderAndPutsWorldlessArchivesLast(t *testing.T) {
	entries := []backupEntry{
		{Name: "a", archiveInfo: archiveInfo{World: "TheIsland.ark", Map: "TheIsland", SaveGame: "SavedArks"}},
		{Name: "b"},
		{Name: "c", archiveInfo: archiveInfo{World: "TheCenter.ark", Map: "TheCenter", SaveGame: "SavedArks"}},
		{Name: "d", archiveInfo: archiveInfo{World: "TheIsland.ark", Map: "TheIsland", SaveGame: "SavedArks"}},
	}
	groups := groupBackups(entries, "TheIsland", "SavedArks")
	if len(groups) != 3 {
		t.Fatalf("%d groups, want 3", len(groups))
	}
	// A group follows its newest entry, so the order of first appearance is the order of groups.
	if groups[0].Map != "TheIsland" || groups[1].Map != "TheCenter" {
		t.Errorf("groups %q, %q", groups[0].Map, groups[1].Map)
	}
	if len(groups[0].Entries) != 2 || groups[0].Entries[0].Name != "a" || groups[0].Entries[1].Name != "d" {
		t.Errorf("island group %+v", groups[0].Entries)
	}
	if groups[2].Map != "" || len(groups[2].Entries) != 1 || groups[2].Entries[0].Name != "b" {
		t.Errorf("worldless group %+v", groups[2])
	}
}

// Two save games of one map are two worlds with two sets of characters. Grouping by map alone put
// them in one table, which is the case the split exists for.
func TestGroupBackupsSeparatesSaveGamesOfTheSameMap(t *testing.T) {
	entries := []backupEntry{
		{Name: "a", archiveInfo: archiveInfo{World: "TheIsland.ark", Map: "TheIsland", SaveGame: "SavedArks"}},
		{Name: "b", archiveInfo: archiveInfo{World: "TheIsland.ark", Map: "TheIsland", SaveGame: "SavedArks2"}},
		{Name: "c", archiveInfo: archiveInfo{World: "TheIsland.ark", Map: "TheIsland", SaveGame: "SavedArks"}},
	}
	groups := groupBackups(entries, "TheIsland", "SavedArks2")
	if len(groups) != 2 {
		t.Fatalf("%d groups, want 2", len(groups))
	}
	if groups[0].SaveGame != "SavedArks" || len(groups[0].Entries) != 2 {
		t.Errorf("first group %q with %d entries", groups[0].SaveGame, len(groups[0].Entries))
	}
	if groups[1].SaveGame != "SavedArks2" || len(groups[1].Entries) != 1 {
		t.Errorf("second group %q with %d entries", groups[1].SaveGame, len(groups[1].Entries))
	}
	// The one being played opens, not the one that happens to be newest.
	if groups[0].Open || !groups[1].Open {
		t.Errorf("open flags %v, %v, want the active save game open", groups[0].Open, groups[1].Open)
	}
}

// An unstamped archive predates the stamp. Filing it under the default save game would be a guess
// dressed up as an answer, so it gets a group of its own and that group never counts as active.
func TestGroupBackupsKeepsUnstampedArchivesOutOfEverySaveGame(t *testing.T) {
	entries := []backupEntry{
		{Name: "old", archiveInfo: archiveInfo{World: "TheIsland.ark", Map: "TheIsland"}},
		{Name: "new", archiveInfo: archiveInfo{World: "TheIsland.ark", Map: "TheIsland", SaveGame: "SavedArks"}},
	}
	groups := groupBackups(entries, "TheIsland", "SavedArks")
	if len(groups) != 2 {
		t.Fatalf("%d groups, want 2", len(groups))
	}
	if groups[0].SaveGame != "" || groups[0].Entries[0].Name != "old" {
		t.Errorf("unstamped group %+v", groups[0])
	}
	if groups[0].Open || !groups[1].Open {
		t.Errorf("open flags %v, %v, want the stamped active group open", groups[0].Open, groups[1].Open)
	}
}

// Without a readable instance file there is no active save game, and the page must not open on
// nothing but headers.
func TestGroupBackupsOpensTheFirstGroupWhenNoneIsActive(t *testing.T) {
	entries := []backupEntry{
		{Name: "a", archiveInfo: archiveInfo{World: "TheCenter.ark", Map: "TheCenter", SaveGame: "SavedArks"}},
		{Name: "b", archiveInfo: archiveInfo{World: "TheIsland.ark", Map: "TheIsland", SaveGame: "SavedArks"}},
	}
	groups := groupBackups(entries, "", "")
	if !groups[0].Open || groups[1].Open {
		t.Errorf("open flags %v, %v, want only the first", groups[0].Open, groups[1].Open)
	}
}

func TestBackupGroupLatestIsTheNewestEntry(t *testing.T) {
	g := backupGroup{Entries: []backupEntry{{Time: "2026-07-28 06:14"}, {Time: "2026-07-28 00:14"}}}
	if g.Latest() != "2026-07-28 06:14" {
		t.Errorf("latest %q", g.Latest())
	}
	if (backupGroup{}).Latest() != "" {
		t.Error("an empty group has no latest entry")
	}
}

func TestArchiveCacheRereadsWhenTheFileChanges(t *testing.T) {
	cfg := config{dataDir: t.TempDir(), archives: &archiveCache{}}
	archive := filepath.Join(backupDir(cfg), "2026-07-26", "main.tar")
	writeArchive(t, archive, map[string]string{"stamp/TheIsland.ark": "world"})

	if entries, err := listBackups(cfg); err != nil || entries[0].Map != "TheIsland" {
		t.Fatalf("first listing: %v, %+v", err, entries)
	}
	// Rewriting the file under the same name has to invalidate the entry. Size and modification time
	// are the key for exactly this case; remembering the old answer would show a world that is no
	// longer in there. A second member makes the size differ for certain, so the test does not rest
	// on timestamp resolution.
	writeArchive(t, archive, map[string]string{
		"stamp/TheCenter.ark": "a different world entirely",
		"stamp/Game.ini":      "ini",
	})
	entries, err := listBackups(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Map != "TheCenter" {
		t.Errorf("map %q after rewrite, want TheCenter", entries[0].Map)
	}
}
