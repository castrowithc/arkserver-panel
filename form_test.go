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

func readINI(t *testing.T, cfg config, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cfg.dataDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSettingsPageRendersEveryField(t *testing.T) {
	rec := get(t, newRouter(filesFixture(t)), "/settings")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`name="gameusersettings.xpmultiplier"`, // a value the fixture has
		`name="game.matingintervalmultiplier"`, // one from the still empty Game.ini
		"Gameplay und Chat",                    // the group headings
		"nicht gesetzt",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	if n := strings.Count(body, `class="row`); n != len(settingFields) {
		t.Errorf("want %d rows, got %d", len(settingFields), n)
	}
}

// The filter field must sit outside the form. Inside it, Enter submits the page of settings
// instead of filtering, which is how a keystroke in a search box came to save the server's config.
func TestTheFilterFieldSitsOutsideTheForm(t *testing.T) {
	body := get(t, newRouter(filesFixture(t)), "/settings").Body.String()

	filter := strings.Index(body, `id="filter"`)
	start := strings.Index(body, `<form method="post" action="/settings/save"`)
	end := strings.Index(body, "</form>")
	if filter < 0 || start < 0 || end < 0 {
		t.Fatalf("page layout not recognised: filter=%d form=%d..%d", filter, start, end)
	}
	if filter > start && filter < end {
		t.Error("the filter field is inside the form again, so Enter saves instead of filtering")
	}
}

// The rows are laid out as a grid, and an author's display rule beats the browser's own handling of
// the hidden attribute. Without an explicit rule the filter marks rows as hidden and they stay on
// the page anyway, which is exactly what happened once.
func TestHiddenRowsAreActuallyHidden(t *testing.T) {
	body := get(t, newRouter(filesFixture(t)), "/settings").Body.String()
	if !strings.Contains(body, "[hidden] { display: none !important; }") {
		t.Error("the stylesheet no longer forces hidden elements out of the layout")
	}
}

// Restarting has to be reachable from anywhere, not only from the bottom of a page that runs to two
// hundred fields.
func TestEveryPageOffersRestartInTheFixedHead(t *testing.T) {
	cfg := filesFixture(t)
	cfg.rcon = rconConfig{addr: "ark:27020", pass: "x"}
	router := newRouter(cfg)

	for _, path := range []string{"/", "/settings", "/files", "/logs"} {
		body := get(t, router, path).Body.String()
		head := body[:strings.Index(body, "</header>")+9]
		if !strings.Contains(head, `action="/restart"`) {
			t.Errorf("%s has no restart button in its head", path)
		}
	}
	// The settings page reaches its save button from up there too.
	if !strings.Contains(get(t, router, "/settings").Body.String(), `form="settings-form"`) {
		t.Error("the head should carry the save button of the settings form")
	}
}

// Without an RCON credential there is nothing to offer, and a button that cannot work is worse than
// none.
func TestNoRestartButtonWithoutRCON(t *testing.T) {
	body := get(t, newRouter(filesFixture(t)), "/settings").Body.String()
	if strings.Contains(body[:strings.Index(body, "</header>")], `action="/restart"`) {
		t.Error("a restart is offered although the panel cannot perform it")
	}
}

// The value in the file is what the form shows. Anything else would be a second truth.
func TestSettingsPageShowsTheFileValue(t *testing.T) {
	cfg := filesFixture(t)
	body := get(t, newRouter(cfg), "/settings").Body.String()
	if !strings.Contains(body, `id="gameusersettings.xpmultiplier" value="1.0"`) {
		t.Error("XPMultiplier should carry the value from the file")
	}
}

func TestSaveWritesOnlyWhatChanged(t *testing.T) {
	cfg := filesFixture(t)
	cfg.pending = &restartFlag{}
	router := newRouter(cfg)

	rec := postForm(t, router, "/settings/save", url.Values{
		"gameusersettings.xpmultiplier": {"2.5"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want a redirect, got %d: %s", rec.Code, rec.Body)
	}
	if loc := rec.Header().Get("Location"); loc != "/settings?saved=1" {
		t.Errorf("want one change reported, got %q", loc)
	}
	if got := readINI(t, cfg, "GameUserSettings.ini"); got != "[ServerSettings]\nXPMultiplier=2.5\n" {
		t.Errorf("file reads %q", got)
	}
	// The reminder has to be up now: the value is on disk but not yet in the running server.
	if !cfg.pending.get() {
		t.Error("a save must raise the restart reminder")
	}
}

// Submitting the form unchanged must not touch anything, or the reminder would cry wolf.
func TestSaveWithoutAChangeWritesNothing(t *testing.T) {
	cfg := filesFixture(t)
	cfg.pending = &restartFlag{}
	before := readINI(t, cfg, "GameUserSettings.ini")

	rec := postForm(t, newRouter(cfg), "/settings/save", url.Values{
		"gameusersettings.xpmultiplier": {"1.0"},
	})
	if loc := rec.Header().Get("Location"); loc != "/settings?saved=0" {
		t.Errorf("want nothing reported as saved, got %q", loc)
	}
	if got := readINI(t, cfg, "GameUserSettings.ini"); got != before {
		t.Errorf("the file changed to %q", got)
	}
	if cfg.pending.get() {
		t.Error("nothing was written, so nothing is pending")
	}
}

// A key the file does not carry yet has to land in its own section, and in the empty Game.ini that
// section does not exist either.
func TestSaveCreatesTheKeyAndTheSection(t *testing.T) {
	cfg := filesFixture(t)
	router := newRouter(cfg)

	postForm(t, router, "/settings/save", url.Values{
		"gameusersettings.tamingspeedmultiplier": {"5"},
		"game.matingintervalmultiplier":          {"0.5"},
	})

	if got := readINI(t, cfg, "GameUserSettings.ini"); got != "[ServerSettings]\nXPMultiplier=1.0\nTamingSpeedMultiplier=5\n" {
		t.Errorf("GameUserSettings.ini reads %q", got)
	}
	want := "[/script/shootergame.shootergamemode]\nMatingIntervalMultiplier=0.5\n"
	if got := readINI(t, cfg, "Game.ini"); got != want {
		t.Errorf("Game.ini reads %q, want %q", got, want)
	}
}

// Emptying a field means the operator takes the setting back, so the line leaves the file rather
// than being written as some default. Here it was the section's only key, so the header goes with
// it: the panel writes a header when it needs one and must not leave one behind.
func TestSaveRemovesTheKeyWhenTheFieldIsCleared(t *testing.T) {
	cfg := filesFixture(t)
	postForm(t, newRouter(cfg), "/settings/save", url.Values{
		"gameusersettings.xpmultiplier": {""},
	})
	if got := readINI(t, cfg, "GameUserSettings.ini"); got != "" {
		t.Errorf("file reads %q", got)
	}
}

func TestToggleWritesTheGamesSpelling(t *testing.T) {
	cfg := filesFixture(t)
	postForm(t, newRouter(cfg), "/settings/save", url.Values{
		"gameusersettings.serverpve": {"true"},
	})
	if got := readINI(t, cfg, "GameUserSettings.ini"); !strings.Contains(got, "ServerPVE=True") {
		t.Errorf("want the capitalised truth value, file reads %q", got)
	}
}

// One bad field stops the whole save. A page of settings is one operation to the operator, and half
// of it applied would leave the server in a state nobody chose.
func TestSaveRejectsTheWholeFormOnABadValue(t *testing.T) {
	cfg := filesFixture(t)
	before := readINI(t, cfg, "GameUserSettings.ini")

	rec := postForm(t, newRouter(cfg), "/settings/save", url.Values{
		"gameusersettings.tamingspeedmultiplier": {"5"},
		"gameusersettings.xpmultiplier":          {"minus eins"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want the form back with a complaint, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "keine Zahl") {
		t.Error("the page should say what is wrong with the field")
	}
	if !strings.Contains(body, `value="minus eins"`) {
		t.Error("the submitted value should survive, so it can be corrected")
	}
	if got := readINI(t, cfg, "GameUserSettings.ini"); got != before {
		t.Errorf("nothing may be written, file reads %q", got)
	}
}

func TestSaveRejectsAValueOutOfRange(t *testing.T) {
	cfg := filesFixture(t)
	rec := postForm(t, newRouter(cfg), "/settings/save", url.Values{
		// AutoSavePeriodMinutes runs from 5 to 180 per the catalogue.
		"gameusersettings.autosaveperiodminutes": {"3"},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want the value refused, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Minimum") {
		t.Error("the page should name the bound that was missed")
	}
}

// A key the game repeats cannot belong to a single form field, so the page shows it read-only and
// the save leaves it alone even if a value is posted for it.
func TestARepeatedKeyIsLockedAndUntouched(t *testing.T) {
	cfg := filesFixture(t)
	path := filepath.Join(cfg.dataDir, "GameUserSettings.ini")
	doubled := "[ServerSettings]\nXPMultiplier=1.0\nXPMultiplier=2.0\n"
	if err := os.WriteFile(path, []byte(doubled), 0o600); err != nil {
		t.Fatal(err)
	}
	router := newRouter(cfg)

	body := get(t, router, "/settings").Body.String()
	if !strings.Contains(body, "steht 2 mal in der Datei") {
		t.Error("the page should explain why the field is locked")
	}

	postForm(t, router, "/settings/save", url.Values{"gameusersettings.xpmultiplier": {"9"}})
	if got := readINI(t, cfg, "GameUserSettings.ini"); got != doubled {
		t.Errorf("the locked key was written after all: %q", got)
	}
}

// The form only carries what it renders. A request that omits a field must not read that as "unset
// it": that is how a filtered page or a partial submit would wipe settings.
func TestAFieldMissingFromTheSubmissionIsLeftAlone(t *testing.T) {
	cfg := filesFixture(t)
	postForm(t, newRouter(cfg), "/settings/save", url.Values{
		"gameusersettings.tamingspeedmultiplier": {"5"},
	})
	if got := readINI(t, cfg, "GameUserSettings.ini"); !strings.Contains(got, "XPMultiplier=1.0") {
		t.Errorf("the untouched setting disappeared: %q", got)
	}
}

func TestSaveNeedsPostAndSameOrigin(t *testing.T) {
	cfg := filesFixture(t)
	router := newRouter(cfg)

	if rec := get(t, router, "/settings/save"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405 on GET, got %d", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/settings/save", strings.NewReader("gameusersettings.xpmultiplier=9"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("want a cross-site post refused, got %d", rec.Code)
	}
}

// The panel must render without the volume too, greyed out and saying why, rather than failing.
func TestUnreadableFileIsReportedNotFatal(t *testing.T) {
	cfg := filesFixture(t)
	if err := os.Remove(filepath.Join(cfg.dataDir, "GameUserSettings.ini")); err != nil {
		t.Fatal(err)
	}
	// A directory in its place is unreadable in a way that is not "does not exist".
	if err := os.Mkdir(filepath.Join(cfg.dataDir, "GameUserSettings.ini"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := get(t, newRouter(cfg), "/settings")
	if rec.Code != http.StatusOK {
		t.Fatalf("want the page to render anyway, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "nicht lesbar") {
		t.Error("the page should name the file it could not read")
	}
}
