package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeDocker stands in for the socket proxy and records which paths were asked for, so the tests
// also pin the exact endpoints the proxy allowlist has to cover.
func fakeDocker(t *testing.T, handler http.HandlerFunc) (dockerConfig, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return dockerConfig{host: srv.URL, container: "ark", timeout: 2 * time.Second}, &seen
}

func TestDockerState(t *testing.T) {
	cfg, seen := fakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}},"RestartCount":0}`))
	})
	st, err := cfg.state()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if !st.State.Running || st.State.Health == nil || st.State.Health.Status != "healthy" {
		t.Errorf("unexpected state: %+v", st.State)
	}
	if (*seen)[0] != "GET /containers/ark/json" {
		t.Errorf("unexpected path %q", (*seen)[0])
	}
}

// A container without a healthcheck has no Health object at all, which must not panic.
func TestDockerStateWithoutHealth(t *testing.T) {
	cfg, _ := fakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"State":{"Status":"running","Running":true}}`))
	})
	st, err := cfg.state()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.State.Health != nil {
		t.Errorf("want no health block, got %+v", st.State.Health)
	}
}

func TestDockerUsage(t *testing.T) {
	stats := dockerStats{}
	stats.CPUStats.CPUUsage.TotalUsage = 2_000_000
	stats.PreCPUStats.CPUUsage.TotalUsage = 1_000_000
	stats.CPUStats.SystemUsage = 20_000_000
	stats.PreCPUStats.SystemUsage = 10_000_000
	stats.CPUStats.OnlineCPUs = 4
	stats.MemoryStats.Usage = 8 << 30
	stats.MemoryStats.Limit = 12 << 30
	stats.MemoryStats.Stats.InactiveFile = 2 << 30

	cfg, seen := fakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(stats)
	})
	cpu, used, limit, err := cfg.usage()
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	// 1e6/1e7 of the machine, across 4 cores, is 40 percent.
	if cpu < 39.9 || cpu > 40.1 {
		t.Errorf("want ~40%% cpu, got %v", cpu)
	}
	// Reclaimable page cache does not count as used memory.
	if used != 6<<30 {
		t.Errorf("want 6 GiB used, got %d", used>>30)
	}
	if limit != 12<<30 {
		t.Errorf("want 12 GiB limit, got %d", limit>>30)
	}
	if (*seen)[0] != "GET /containers/ark/stats?stream=false" {
		t.Errorf("unexpected path %q", (*seen)[0])
	}
}

// The very first sample after a container start has no previous one to compare against, which would
// make the delta negative or zero. That must read as 0 percent, not as a wild number.
func TestDockerCPUWithoutPreviousSample(t *testing.T) {
	var s dockerStats
	s.CPUStats.CPUUsage.TotalUsage = 5_000_000
	s.CPUStats.SystemUsage = 10_000_000
	if got := cpuPercentOf(s); got != 0 {
		t.Errorf("want 0, got %v", got)
	}
}

func TestDockerStartStopPaths(t *testing.T) {
	cfg, seen := fakeDocker(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	if err := cfg.start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := cfg.stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	want := []string{"POST /containers/ark/start", "POST /containers/ark/stop"}
	for i, w := range want {
		if (*seen)[i] != w {
			t.Errorf("want %q, got %q", w, (*seen)[i])
		}
	}
}

// Anything the proxy allowlist does not cover comes back as 403, and the message has to say so or
// the cause is impossible to find.
func TestDockerReportsRejection(t *testing.T) {
	cfg, _ := fakeDocker(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden by allowlist", http.StatusForbidden)
	})
	_, err := cfg.state()
	if err == nil {
		t.Fatal("want an error on 403, got none")
	}
	if got := err.Error(); !contains(got, "403") || !contains(got, "allowlist") {
		t.Errorf("error should carry status and body, got %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
