package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const envFixture = `# Copy to .env in THIS directory.
SESSION_NAME=My ARK SE Server
SERVER_MAP=TheIsland
MAX_PLAYERS=2
SERVER_PASSWORD=geheim
ADMIN_PASSWORD=auch-geheim
GAME_MOD_IDS=
ENABLE_GAME_LOG=false
ARK_SERVER_VOLUME=ark-data
MEM_LIMIT=12g
`

func envFixtureCfg(t *testing.T) config {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return config{user: "admin", pass: "secret", envDir: dir, dataDir: t.TempDir()}
}

// The defect this page exists for: arkmanager writes these keys into GameUserSettings.ini from the
// .env at every start, so a form field for one of them takes an edit, reports it saved, and loses it
// at the next start without a word.
func TestEnvManagedKeysAreLockedInTheForm(t *testing.T) {
	groups := buildGroups(map[string]*iniSource{
		"gameusersettings": {entry: fileEntry{ID: "gameusersettings"}, ini: parseINI("[SessionSettings]\nSessionName=Aus der Datei\n")},
		"game":             {entry: fileEntry{ID: "game"}, ini: parseINI("")},
	})

	found := false
	for _, g := range groups {
		for _, row := range g.Rows {
			if _, managed := envManagedKeys[row.Key]; !managed {
				continue
			}
			found = true
			if row.Locked == "" {
				t.Errorf("%s is editable although the deployment overwrites it", row.Key)
			}
			if !strings.Contains(row.Locked, "Start") {
				t.Errorf("%s locks with %q, which does not say when it is overwritten", row.Key, row.Locked)
			}
		}
	}
	if !found {
		t.Fatal("no managed key in the catalogue; this test would pass vacuously")
	}
}

// The value still shows: read-only is the point, hiding it is not.
func TestALockedManagedKeyStillShowsItsValue(t *testing.T) {
	groups := buildGroups(map[string]*iniSource{
		"gameusersettings": {entry: fileEntry{ID: "gameusersettings"}, ini: parseINI("[SessionSettings]\nSessionName=Aus der Datei\n")},
		"game":             {entry: fileEntry{ID: "game"}, ini: parseINI("")},
	})
	for _, g := range groups {
		for _, row := range g.Rows {
			if row.Key == "SessionName" {
				if row.Value != "Aus der Datei" {
					t.Errorf("SessionName shows %q", row.Value)
				}
				return
			}
		}
	}
	t.Fatal("SessionName is not in the catalogue")
}

// applyForm skips locked rows, so a submission naming one writes nothing. Without this the lock
// would be decoration.
func TestSubmittingAManagedKeyWritesNothing(t *testing.T) {
	sources := map[string]*iniSource{
		"gameusersettings": {entry: fileEntry{ID: "gameusersettings"}, ini: parseINI("[SessionSettings]\nSessionName=Alt\n")},
		"game":             {entry: fileEntry{ID: "game"}, ini: parseINI("")},
	}
	groups := buildGroups(sources)
	changed, ok := applyForm(sources, map[string][]string{"gameusersettings.sessionname": {"Neu"}}, groups)
	if !ok || changed != 0 {
		t.Errorf("changed=%d ok=%v, want 0 and no failure", changed, ok)
	}
	if got := sources["gameusersettings"].ini.render(); !strings.Contains(got, "SessionName=Alt") {
		t.Errorf("the value was written after all:\n%s", got)
	}
}

func TestDeploymentPageListsTheValuesWithTheirOwner(t *testing.T) {
	cfg := envFixtureCfg(t)
	values, err := loadEnvValues(cfg)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]envValue{}
	for _, v := range values {
		byKey[v.Key] = v
	}
	if got := byKey["SESSION_NAME"].Value; got != "My ARK SE Server" {
		t.Errorf("SESSION_NAME reads %q", got)
	}
	if byKey["GAME_MOD_IDS"].Value != "" || byKey["GAME_MOD_IDS"].Set {
		t.Error("an empty key should read as empty")
	}
	// Structural keys belong to the host and stay off this page.
	for _, key := range []string{"ARK_SERVER_VOLUME", "MEM_LIMIT", "PUID"} {
		if _, ok := byKey[key]; ok {
			t.Errorf("%s should not be listed", key)
		}
	}
	for _, v := range values {
		if v.Effect == "" {
			t.Errorf("%s is listed without saying what it does", v.Key)
		}
	}
}

func TestDeploymentPageNeverShowsAPassword(t *testing.T) {
	cfg := envFixtureCfg(t)
	values, err := loadEnvValues(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		if v.Secret && v.Value != "" {
			t.Errorf("%s carries its value", v.Key)
		}
	}
	// And a configured password is still reported as configured, or the page would read as if none
	// were set.
	for _, v := range values {
		if v.Key == "ADMIN_PASSWORD" && !v.Set {
			t.Error("a password that is set should read as set")
		}
	}

	rec := get(t, newRouter(cfg), "/env")
	if strings.Contains(rec.Body.String(), "geheim") {
		t.Error("a password reached the rendered page")
	}
}
