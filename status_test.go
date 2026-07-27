package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The lifecycle label is the one piece of judgement on the page: neither signal alone can tell a
// stopped server from one that is still loading its world.
func TestLifecycleOf(t *testing.T) {
	tests := []struct {
		name                                         string
		dockerKnown, running, rconConfigured, rconOK bool
		want, wantLabel                              string
	}{
		{"rcon answers", true, true, true, true, "laeuft", "läuft"},
		{"container down", true, false, true, false, "gestoppt", "gestoppt"},
		{"container up but game silent", true, true, true, false, "startet", "startet, Welt wird geladen"},
		{"no docker, rcon answers", false, false, true, true, "laeuft", "läuft"},
		{"nothing reachable", false, false, true, false, "unbekannt", "nicht erreichbar"},
		// A stopped container cannot have an answering game process; trust RCON and do not claim
		// the server is down while it is plainly serving.
		{"stale container read", true, false, true, true, "laeuft", "läuft"},
		// Without an RCON credential nobody asked the game, so "loading" would be a guess. Seen
		// for real while exercising start/stop with RCON left unconfigured.
		{"running but rcon not configured", true, true, false, false, "unbekannt", "Container läuft, Spielstatus unbekannt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, label := lifecycleOf(tt.dockerKnown, tt.running, tt.rconConfigured, tt.rconOK)
			if got != tt.want || label != tt.wantLabel {
				t.Errorf("want %s/%q, got %s/%q", tt.want, tt.wantLabel, got, label)
			}
		})
	}
}

// With nothing configured the page must still render and say why the figures are missing, rather
// than fail the request.
func TestGatherStatusDegrades(t *testing.T) {
	st := gatherStatus(config{dataDir: t.TempDir()}, "127.0.0.1:8080")
	if st.Lifecycle != "unbekannt" {
		t.Errorf("want unbekannt, got %s", st.Lifecycle)
	}
	if st.CanStartStop {
		t.Error("start/stop must not be offered without docker access")
	}
	if st.PlayersKnown {
		t.Error("players must not read as known without rcon")
	}
	if len(st.Notices) != 3 {
		t.Errorf("want a notice for docker, rcon and version, got %q", st.Notices)
	}
}

func TestGatherStatusWithDockerAndRCON(t *testing.T) {
	docker, _ := fakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/stats") {
			w.Write([]byte(`{"cpu_stats":{"cpu_usage":{"total_usage":2000000},"system_cpu_usage":20000000,"online_cpus":4},
			                 "precpu_stats":{"cpu_usage":{"total_usage":1000000},"system_cpu_usage":10000000},
			                 "memory_stats":{"usage":8589934592,"limit":12884901888,"stats":{"inactive_file":0}}}`))
			return
		}
		w.Write([]byte(`{"State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}}}`))
	})
	rconAddr := fakeRCON(t, true, map[string]string{"ListPlayers": "0. Alice, 1\n1. Bob, 2"})

	cfg := config{
		dataDir: writeManifest(t, manifestFixture),
		docker:  docker,
		rcon:    testConfig(rconAddr),
	}
	st := gatherStatus(cfg, "127.0.0.1:8080")

	if st.Lifecycle != "laeuft" {
		t.Errorf("want laeuft, got %s (%q)", st.Lifecycle, st.Notices)
	}
	if st.CPUPercent != "40.0" {
		t.Errorf("want 40.0 cpu, got %s", st.CPUPercent)
	}
	if st.MemUsed != "8.0" || st.MemLimit != "12.0" {
		t.Errorf("want 8.0 of 12.0 GB, got %s of %s", st.MemUsed, st.MemLimit)
	}
	if st.PlayerCount != 2 || !st.PlayersKnown {
		t.Errorf("want 2 known players, got %d (known=%v)", st.PlayerCount, st.PlayersKnown)
	}
	if st.Build != "21241282" {
		t.Errorf("want the build id, got %s", st.Build)
	}
	if st.Health != "healthy" {
		t.Errorf("want healthy, got %s", st.Health)
	}
	if len(st.Notices) != 0 {
		t.Errorf("want no notices, got %q", st.Notices)
	}
}

// A running container whose game process is not answering is the restart window. It has to read as
// starting, and it must not offer a figure it does not have.
func TestGatherStatusDuringWorldLoad(t *testing.T) {
	docker, _ := fakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/stats") {
			w.Write([]byte(`{"memory_stats":{"usage":1073741824,"limit":12884901888}}`))
			return
		}
		w.Write([]byte(`{"State":{"Status":"running","Running":true}}`))
	})
	// Accepts the connection, never answers: exactly what ARK does while it reloads.
	ln := silentListener(t)

	cfg := config{
		dataDir: writeManifest(t, manifestFixture),
		docker:  docker,
		rcon: rconConfig{
			addr: ln, pass: "secret",
			dialTimeout: time.Second, statusBudget: 200 * time.Millisecond, commandBudget: time.Second,
		},
	}
	st := gatherStatus(cfg, "127.0.0.1:8080")
	if st.Lifecycle != "startet" {
		t.Errorf("want startet, got %s", st.Lifecycle)
	}
	if st.PlayersKnown {
		t.Error("player count must stay unknown while the game is silent")
	}
}

func TestStatusRoutesRenderAndGuard(t *testing.T) {
	cfg := config{user: "admin", pass: "secret", dataDir: t.TempDir()}
	router := newRouter(cfg)

	// The fragment and the page both render, even with nothing configured.
	for _, path := range []string{"/", "/status"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetBasicAuth("admin", "secret")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: want 200, got %d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Neu starten") {
			t.Errorf("GET %s: want the lifecycle bar in the output", path)
		}
	}

	// Lifecycle actions reject anything but a same-origin POST.
	tests := []struct {
		name   string
		method string
		origin string
		fetch  string
		want   int
	}{
		{"GET is refused", http.MethodGet, "", "", http.StatusMethodNotAllowed},
		{"cross-site POST is refused", http.MethodPost, "https://evil.example", "cross-site", http.StatusForbidden},
		{"cross-origin by header is refused", http.MethodPost, "https://evil.example", "", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/stop", nil)
			req.SetBasicAuth("admin", "secret")
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.fetch != "" {
				req.Header.Set("Sec-Fetch-Site", tt.fetch)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Errorf("want %d, got %d", tt.want, rec.Code)
			}
		})
	}
}
