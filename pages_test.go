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
