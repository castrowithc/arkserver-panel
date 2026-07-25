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

func postForm(t *testing.T, router http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// A zero-value config carries no flag pointer; every call has to survive that rather than panic.
func TestRestartFlagNilIsHarmless(t *testing.T) {
	var f *restartFlag
	f.set()
	f.clear()
	if f.get() {
		t.Error("a nil flag must read as nothing pending")
	}
}

func TestSaveRaisesTheReminderAndRestartClearsIt(t *testing.T) {
	cfg := filesFixture(t)
	cfg.pending = &restartFlag{}
	// An RCON server that answers, so the restart action can actually run.
	cfg.rcon = testConfig(fakeRCON(t, true, map[string]string{"SaveWorld": "ok", "DoExit": "ok"}))
	router := newRouter(cfg)

	if strings.Contains(get(t, router, "/files").Body.String(), "noch nicht in Kraft") {
		t.Fatal("nothing is pending before the first save")
	}

	postForm(t, router, "/files/save", url.Values{"f": {"game"}, "content": {"x\n"}})
	if !cfg.pending.get() {
		t.Fatal("a save must raise the reminder")
	}

	// The reminder shows on both pages, with the offer to restart on the files page.
	files := get(t, router, "/files").Body.String()
	if !strings.Contains(files, "noch nicht in Kraft") || !strings.Contains(files, "Jetzt neu starten") {
		t.Error("the files page should carry the reminder and the restart button")
	}
	if !strings.Contains(get(t, router, "/").Body.String(), "noch nicht in Kraft") {
		t.Error("the monitor should carry the reminder too")
	}

	if rec := postForm(t, router, "/restart", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("restart: want 303, got %d: %s", rec.Code, rec.Body)
	}
	if cfg.pending.get() {
		t.Error("a restart must clear the reminder")
	}
}

// Ticking the box saves and restarts in one step; leaving it alone must never restart.
func TestAutoRestartOnlyWhenAsked(t *testing.T) {
	newCfg := func(t *testing.T) (config, http.Handler, *[]string) {
		t.Helper()
		var commands []string
		addr := recordingRCON(t, &commands)
		cfg := filesFixture(t)
		cfg.pending = &restartFlag{}
		cfg.rcon = testConfig(addr)
		return cfg, newRouter(cfg), &commands
	}

	t.Run("unticked leaves the server alone", func(t *testing.T) {
		cfg, router, commands := newCfg(t)
		postForm(t, router, "/files/save", url.Values{"f": {"game"}, "content": {"a\n"}})
		if len(*commands) != 0 {
			t.Errorf("no restart was asked for, yet RCON saw %q", *commands)
		}
		if !cfg.pending.get() {
			t.Error("the reminder should stand instead")
		}
	})

	t.Run("ticked saves and restarts", func(t *testing.T) {
		cfg, router, commands := newCfg(t)
		rec := postForm(t, router, "/files/save", url.Values{"f": {"game"}, "content": {"b\n"}, "restart": {"1"}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("want 303, got %d: %s", rec.Code, rec.Body)
		}
		if loc := rec.Header().Get("Location"); !strings.Contains(loc, "restarted=1") {
			t.Errorf("want the restarted notice, got %q", loc)
		}
		if len(*commands) != 2 || (*commands)[0] != "SaveWorld" || (*commands)[1] != "DoExit" {
			t.Errorf("want SaveWorld then DoExit, got %q", *commands)
		}
		if cfg.pending.get() {
			t.Error("the reminder should be cleared by the restart it just performed")
		}
		// The file still has to carry the new content.
		got, err := os.ReadFile(filepath.Join(cfg.dataDir, "Game.ini"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "b\n" {
			t.Errorf("file content is %q", got)
		}
	})
}

// Without an RCON credential the panel cannot restart, so it must not offer to.
func TestNoRestartOfferWithoutRCON(t *testing.T) {
	cfg := filesFixture(t)
	cfg.pending = &restartFlag{}
	router := newRouter(cfg)

	postForm(t, router, "/files/save", url.Values{"f": {"game"}, "content": {"x\n"}})
	body := get(t, router, "/files").Body.String()

	if !strings.Contains(body, "noch nicht in Kraft") {
		t.Error("the reminder still applies")
	}
	if strings.Contains(body, "Jetzt neu starten") || strings.Contains(body, "automatisch neu starten") {
		t.Error("neither the button nor the checkbox may appear without RCON")
	}
}

// recordingRCON is a fake server that records the commands it received.
func recordingRCON(t *testing.T, into *[]string) string {
	t.Helper()
	return fakeRCONFunc(t, func(cmd string) string {
		*into = append(*into, cmd)
		return "ok"
	})
}
