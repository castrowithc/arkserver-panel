package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// filesFixture lays out the two directories the panel mounts, with the content each side really
// has: an INI on the volume and the deployment's .env next to the compose file.
func filesFixture(t *testing.T) config {
	t.Helper()
	dataDir, envDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "GameUserSettings.ini"), []byte("[ServerSettings]\nXPMultiplier=1.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "Game.ini"), []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, ".env"), []byte("SESSION_NAME=Test\nADMIN_PASSWORD=hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "log", "arkmanager.log"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return config{user: "admin", pass: "secret", dataDir: dataDir, envDir: envDir}
}

func get(t *testing.T, router http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestFilesPageShowsAndEdits(t *testing.T) {
	cfg := filesFixture(t)
	router := newRouter(cfg)

	rec := get(t, router, "/files?f=gameusersettings")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "XPMultiplier=1.0") {
		t.Error("the editor should show the file content")
	}
	if !strings.Contains(body, "Speichern") {
		t.Error("an editable file needs a save button")
	}
}

// The .env is read-only and its credentials must not reach the browser in the clear.
func TestFilesPageMasksTheEnv(t *testing.T) {
	router := newRouter(filesFixture(t))
	body := get(t, router, "/files?f=env").Body.String()

	if strings.Contains(body, "hunter2") {
		t.Error("the admin password reached the page")
	}
	if !strings.Contains(body, "SESSION_NAME=Test") {
		t.Error("non-secret values should stay visible")
	}
	if strings.Contains(body, "Speichern") {
		t.Error("a read-only file must not offer a save button")
	}
}

func TestSaveWritesThroughAndRedirects(t *testing.T) {
	cfg := filesFixture(t)
	router := newRouter(cfg)

	form := url.Values{"f": {"gameusersettings"}, "content": {"[ServerSettings]\r\nXPMultiplier=2.0\r\n"}}
	req := httptest.NewRequest(http.MethodPost, "/files/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want a redirect after saving, got %d: %s", rec.Code, rec.Body)
	}
	got, err := os.ReadFile(filepath.Join(cfg.dataDir, "GameUserSettings.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "[ServerSettings]\nXPMultiplier=2.0\n" {
		t.Errorf("file content is %q", got)
	}
}

// Saving is only offered for the two INIs. The .env must be refused even if someone posts its id
// directly, since the page never offers it.
func TestSaveRefusesReadOnlyFile(t *testing.T) {
	cfg := filesFixture(t)
	router := newRouter(cfg)

	form := url.Values{"f": {"env"}, "content": {"ADMIN_PASSWORD=changed\n"}}
	req := httptest.NewRequest(http.MethodPost, "/files/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
	got, err := os.ReadFile(filepath.Join(cfg.envDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "hunter2") {
		t.Error("the .env was modified despite being read-only")
	}
}

func TestLogsPageAndRawMode(t *testing.T) {
	router := newRouter(filesFixture(t))

	rec := get(t, router, "/logs?f=arkmanager")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "one\ntwo") {
		t.Error("the viewer should show the log lines")
	}

	raw := get(t, router, "/logs?f=arkmanager&raw=1")
	if ct := raw.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("the refresh should return plain text, got %q", ct)
	}
	if raw.Body.String() != "one\ntwo" {
		t.Errorf("raw body is %q", raw.Body.String())
	}
}

// A file that is not there is normal here: the game log only appears once it is enabled. The page
// must say so rather than fail.
func TestMissingFileIsReported(t *testing.T) {
	router := newRouter(filesFixture(t))
	rec := get(t, router, "/logs?f=gamelog")

	if rec.Code != http.StatusOK {
		t.Fatalf("want the page to render anyway, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "nicht lesbar") {
		t.Error("the page should name the missing file")
	}
}

// An unknown id falls back to the first entry instead of erroring, so a stale bookmark still lands
// somewhere sensible.
func TestUnknownIDFallsBack(t *testing.T) {
	router := newRouter(filesFixture(t))
	body := get(t, router, "/files?f=../../etc/passwd").Body.String()
	if !strings.Contains(body, "XPMultiplier") {
		t.Error("want the first file rendered as a fallback")
	}
}

func TestSavegamesPageNamesWhatIsLoadedAndWhereItComesFrom(t *testing.T) {
	cfg := filesFixture(t)
	// The instance file as the image ships it: the map still a reference to the environment, so the
	// page has to resolve it and say that the answer came from the host file.
	instances := filepath.Dir(instanceCfgPath(cfg))
	if err := os.MkdirAll(instances, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instanceCfgPath(cfg), []byte(instanceSample), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.envDir, ".env"), []byte("SERVER_MAP=TheIsland\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	arks := filepath.Join(savedRoot(cfg), savedArksDir)
	if err := os.MkdirAll(arks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(arks, "TheIsland.ark"), []byte("world"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := get(t, newRouter(cfg), "/savegames")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "TheIsland") {
		t.Error("the loaded map should be named")
	}
	if !strings.Contains(body, ".env") {
		t.Error("the page should say where the value came from")
	}
	// Without Docker access the switch cannot be performed, so it must not be offered.
	if strings.Contains(body, "/savegames/switch") {
		t.Error("the switch was offered without Docker access")
	}
}

// The page itself, not just the grouping: two save games of one map have to arrive as two foldable
// groups, and only the one being played may be open. Rendering it here is also what catches a broken
// template, which the grouping tests alone would not.
func TestBackupsPageFoldsEverySaveGameButTheLoadedOne(t *testing.T) {
	cfg := filesFixture(t)
	instances := filepath.Dir(instanceCfgPath(cfg))
	if err := os.MkdirAll(instances, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instanceCfgPath(cfg), []byte(instanceSample), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.envDir, ".env"), []byte("SERVER_MAP=TheIsland\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Three archives of one map: two save games and one from before the stamp existed.
	for _, a := range []struct{ name, saveDir string }{
		{"main.2026-07-28_06.14.12.tar", savedArksDir},
		{"main.2026-07-27_18.02.44.tar", "SavedArks2"},
		{"main.2026-07-20_09.00.00.tar", ""},
	} {
		path := filepath.Join(backupDir(cfg), "2026-07-28", a.name)
		writeArchive(t, path, map[string]string{"stamp/TheIsland.ark": "world"})
		if a.saveDir == "" {
			continue
		}
		if err := os.WriteFile(path+savegameStampSuffix, []byte("savedir="+a.saveDir+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	rec := get(t, newRouter(cfg), "/backups")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if n := strings.Count(body, `<details class="group"`); n != 3 {
		t.Errorf("%d groups, want one per save game plus one for the unstamped archive", n)
	}
	// Exactly one open group, and it is the loaded save game. The default directory is labelled, so
	// the heading says "Standard" rather than repeating the directory name.
	if n := strings.Count(body, `class="group" open`); n != 1 {
		t.Errorf("%d open groups, want exactly the loaded one", n)
	}
	if !strings.Contains(body, "Standard") || !strings.Contains(body, "SavedArks2") {
		t.Error("both save games should be named in a heading")
	}
	if !strings.Contains(body, "Herkunft unbekannt") {
		t.Error("the unstamped archive needs a group that does not claim a save game")
	}
}

func TestSwitchSavegameRefusesWithoutDockerAccess(t *testing.T) {
	cfg := filesFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/savegames/switch",
		strings.NewReader(url.Values{"map": {"TheCenter"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	newRouter(cfg).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("want 403 without Docker access, got %d", rec.Code)
	}
}
