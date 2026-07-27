package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// instanceSample mirrors the shape of the file the server image ships: values as environment
// references, comments aligned in a column, and the save-directory line present but commented out.
const instanceSample = `# arkmanager instance configuration
serverMap=${SERVER_MAP}                                             # server map (default TheIsland)
serverMapModId=${SERVER_MAP_MOD_ID}                                 # Map Mod Id via SERVER_MAP_MOD_ID
ark_SessionName=${SESSION_NAME}                                     # session name
#ark_AltSaveDirectoryName="SotF"                                    # Uncomment to specify a different save directory name
`

func TestParseInstanceFileReadsWhatIsInForce(t *testing.T) {
	f := parseInstanceFile(instanceSample)
	if f.mapValue != "${SERVER_MAP}" {
		t.Errorf("map value %q", f.mapValue)
	}
	// A commented save-directory line is not in force, so the default applies, but the line is still
	// found so it can be uncommented in place later.
	if f.saveActive {
		t.Error("a commented save directory counted as active")
	}
	if got := f.saveDirOrDefault(); got != savedArksDir {
		t.Errorf("save dir %q, want the default", got)
	}
	if f.saveLine < 0 {
		t.Error("the commented save-directory line was not found")
	}
}

func TestParseInstanceFileIgnoresACommentedMap(t *testing.T) {
	f := parseInstanceFile("#serverMap=TheCenter\nserverMap=Ragnarok\n")
	if f.mapValue != "Ragnarok" {
		t.Errorf("map value %q, want the uncommented line to win", f.mapValue)
	}
}

func TestRenderReturnsAnUntouchedFileUnchanged(t *testing.T) {
	if got := parseInstanceFile(instanceSample).render(); got != instanceSample {
		t.Errorf("render changed a file nobody edited:\n%q", got)
	}
}

func TestSetMapChangesExactlyOneLine(t *testing.T) {
	f := parseInstanceFile(instanceSample)
	f.setMap("TheCenter")

	before, after := strings.Split(instanceSample, "\n"), strings.Split(f.render(), "\n")
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
	line := after[1]
	if !strings.HasPrefix(line, "serverMap=TheCenter") {
		t.Errorf("map line is %q", line)
	}
	// The trailing comment is the reason the file is readable at all; a rewrite must not eat it.
	if !strings.Contains(line, "# server map (default TheIsland)") {
		t.Errorf("the comment was lost: %q", line)
	}
}

func TestSetSaveDirUncommentsInPlaceAndCommentsBack(t *testing.T) {
	f := parseInstanceFile(instanceSample)
	f.setSaveDir("zweite-runde")

	lines := strings.Split(f.render(), "\n")
	if len(lines) != len(strings.Split(instanceSample, "\n")) {
		t.Fatalf("a line was added instead of the existing one being used")
	}
	line := lines[4]
	if !strings.HasPrefix(line, `ark_AltSaveDirectoryName="zweite-runde"`) {
		t.Fatalf("save-directory line is %q", line)
	}
	if !strings.Contains(line, "# Uncomment to specify") {
		t.Errorf("the comment was lost: %q", line)
	}
	if got := f.saveDirOrDefault(); got != "zweite-runde" {
		t.Errorf("save dir %q", got)
	}

	// Back to the default: the line goes out of force but keeps its value, so the file still says
	// which directory was last used.
	f.setSaveDir("")
	line = strings.Split(f.render(), "\n")[4]
	if !strings.HasPrefix(line, "#") {
		t.Errorf("clearing left the line in force: %q", line)
	}
	if !strings.Contains(line, "zweite-runde") {
		t.Errorf("clearing dropped the previous name: %q", line)
	}
	if got := f.saveDirOrDefault(); got != savedArksDir {
		t.Errorf("save dir %q, want the default", got)
	}
}

func TestSetSaveDirAppendsWhenTheKeyIsAbsent(t *testing.T) {
	f := parseInstanceFile("serverMap=TheIsland\n")
	f.setSaveDir("zweite")
	if !strings.Contains(f.render(), `ark_AltSaveDirectoryName="zweite"`) {
		t.Errorf("the key was not added:\n%s", f.render())
	}
}

func TestResolveMapPrefersALiteralOverTheEnvironment(t *testing.T) {
	cfg := config{envDir: t.TempDir()}
	writeEnv(t, cfg.envDir, "SERVER_MAP=TheIsland\n")

	origin := resolveMap(cfg, parseInstanceFile("serverMap=Ragnarok\n"))
	if origin.Value != "Ragnarok" || !origin.Exact {
		t.Errorf("literal not authoritative: %+v", origin)
	}
	if !strings.Contains(origin.Source, "Instanzdatei") {
		t.Errorf("source %q does not name the file", origin.Source)
	}
}

func TestResolveMapFallsBackToTheEnvFileAndSaysSo(t *testing.T) {
	cfg := config{envDir: t.TempDir()}
	writeEnv(t, cfg.envDir, "SERVER_MAP=TheCenter\n")

	origin := resolveMap(cfg, parseInstanceFile(instanceSample))
	if origin.Value != "TheCenter" {
		t.Errorf("value %q, want the one from the .env", origin.Value)
	}
	// Without Docker access the answer comes from a file that may have been edited since the
	// container was made, and the page has to be able to say that.
	if origin.Exact {
		t.Error("a value read from the host file claimed to be exact")
	}
	if !strings.Contains(origin.Source, ".env") {
		t.Errorf("source %q does not name where it came from", origin.Source)
	}
}

func TestResolveMapReportsAnUnresolvableReference(t *testing.T) {
	origin := resolveMap(config{envDir: t.TempDir()}, parseInstanceFile("serverMap=${SERVER_MAP}\n"))
	if origin.Value != "" || origin.Exact {
		t.Errorf("an unresolvable reference produced a value: %+v", origin)
	}
}

func TestValidSaveDirRefusesWhatMustNotBecomeAPath(t *testing.T) {
	for _, bad := range []string{"", "..", "../etc", "a/b", "a\\b", ".hidden", "Config", "logs", strings.Repeat("a", maxSaveDirLen+1), "mit leerzeichen", `a"b`, "$SERVER_MAP"} {
		if err := validSaveDir(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, good := range []string{"zweite", "zweite-runde", "Runde_2", "a.b", "SavedArks2"} {
		if err := validSaveDir(good); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}
}

func TestListSaveGamesSeparatesWorldsFromRotationCopies(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	root := savedRoot(cfg)
	write := func(dir, name, content string) {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(savedArksDir, "TheIsland.ark", "world")
	// Everything below shares the directory with the world and is not a save game of its own.
	write(savedArksDir, "TheIsland_25.07.2026_10.42.39.ark", "rotation")
	write(savedArksDir, "TheIsland_AntiCorruptionBackup.bak", "ark's own")
	write(savedArksDir, "76500000000000000.arkprofile", "profile")
	write("zweite", "TheIsland.ark", "second world")
	write("Config", "GameUserSettings.ini", "ini")
	write("Logs", "ShooterGame.log", "log")

	saves, err := listSaveGames(cfg, savedArksDir, "TheIsland")
	if err != nil {
		t.Fatal(err)
	}
	if len(saves) != 2 {
		t.Fatalf("%d save games, want 2: %+v", len(saves), saves)
	}
	if !saves[0].Active || saves[0].Dir != savedArksDir {
		t.Errorf("the loaded save game is not first: %+v", saves[0])
	}
	if saves[1].Dir != "zweite" || saves[1].Active {
		t.Errorf("second entry is %+v", saves[1])
	}
	if saves[0].MapLabel != "The Island" {
		t.Errorf("map label %q", saves[0].MapLabel)
	}
}

func TestListSaveGamesShowsAFreshOneThatHasNotSavedYet(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(savedRoot(cfg), savedArksDir), 0o755); err != nil {
		t.Fatal(err)
	}
	// ARK writes the world at the first save, not when it loads, so right after a switch the
	// directory is empty. Hiding the entry would read as a failed switch.
	saves, err := listSaveGames(cfg, "zweite", "Ragnarok")
	if err != nil {
		t.Fatal(err)
	}
	if len(saves) != 1 || !saves[0].Fresh || !saves[0].Active {
		t.Fatalf("fresh save game missing: %+v", saves)
	}
	if saves[0].Size != "" {
		t.Errorf("a world that does not exist reported a size: %q", saves[0].Size)
	}
}

func TestMapChoicesMarkWhatIsInstalled(t *testing.T) {
	cfg := config{dataDir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(contentDir(cfg), "Maps", "TheIslandSubMaps"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Six of the stock maps ship as official mod maps under Mods rather than Maps.
	if err := os.MkdirAll(filepath.Join(contentDir(cfg), "Mods", "TheCenter"), 0o755); err != nil {
		t.Fatal(err)
	}

	installed := map[string]bool{}
	for _, m := range mapChoices(cfg) {
		installed[m.Code] = m.Installed
	}
	if !installed["TheIsland"] || !installed["TheCenter"] {
		t.Error("an installed map was not recognised")
	}
	if installed["Ragnarok"] || installed["Gen2"] {
		t.Error("a missing map was reported as installed")
	}
}

func TestApplySaveGameRefusesBadInputBeforeTouchingTheServer(t *testing.T) {
	// No Docker access is configured here, so a call that reached the stop would fail with a Docker
	// error instead of the validation message: the wording is the assertion.
	cfg := config{dataDir: t.TempDir()}
	if err := applySaveGame(cfg, "NotAMap", savedArksDir); err == nil || !strings.Contains(err.Error(), "unbekannte Karte") {
		t.Errorf("unknown map: %v", err)
	}
	if err := applySaveGame(cfg, "TheIsland", "../etc"); err == nil || strings.Contains(err.Error(), "docker") {
		t.Errorf("bad directory: %v", err)
	}
}

func writeEnv(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
