// Docker access, strictly through the path-filtered socket proxy: the panel never sees the socket
// itself. Only four calls are made, so the official SDK would be a heavy dependency for nothing.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type dockerConfig struct {
	// host is the proxy's base URL, e.g. http://socket-proxy:2375. Empty means no Docker access
	// was wired up, which is a supported state: the panel then hides what needs it.
	host      string
	container string
	timeout   time.Duration
	// stopTimeout is the budget for the stop call alone, which is a different kind of wait than a
	// status poll: see stop below.
	stopTimeout time.Duration
}

func (c dockerConfig) configured() bool { return c.host != "" && c.container != "" }

// containerState is the slice of Docker's container JSON the monitor actually shows.
type containerState struct {
	// Created is when this container was made, which is not the same as when it was last started:
	// only a recreate moves it. That makes it the honest answer to whether the .env on disk is the
	// one the running server was built from, because those values are frozen in at creation.
	Created time.Time `json:"Created"`
	State   struct {
		Status    string    `json:"Status"`
		Running   bool      `json:"Running"`
		StartedAt time.Time `json:"StartedAt"`
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	RestartCount int `json:"RestartCount"`
}

// dockerStats is Docker's stats document, again reduced to the fields the meters need.
type dockerStats struct {
	CPUStats    cpuStats `json:"cpu_stats"`
	PreCPUStats cpuStats `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"`
		Limit uint64 `json:"limit"`
		Stats struct {
			// Page cache the kernel can reclaim under pressure. Docker's own `docker stats`
			// subtracts it, and without that the figure reads far higher than the memory the
			// server actually needs.
			InactiveFile uint64 `json:"inactive_file"`
		} `json:"stats"`
	} `json:"memory_stats"`
}

type cpuStats struct {
	CPUUsage struct {
		TotalUsage uint64 `json:"total_usage"`
	} `json:"cpu_usage"`
	SystemUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs  uint32 `json:"online_cpus"`
}

func (d dockerConfig) do(method, path string, out any) error {
	client := &http.Client{Timeout: d.timeout}
	url := strings.TrimSuffix(d.host, "/") + path
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("docker: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// The proxy answers 403 for anything outside its allowlist, which is a misconfiguration
		// rather than a server problem, so keep the code in the message.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker: %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (d dockerConfig) state() (containerState, error) {
	var st containerState
	err := d.do(http.MethodGet, "/containers/"+d.container+"/json", &st)
	return st, err
}

// usage reports CPU as a percentage of one core times the core count, the same figure `docker stats`
// prints, and memory as used bytes against the limit.
func (d dockerConfig) usage() (cpuPercent float64, memUsed, memLimit uint64, err error) {
	var s dockerStats
	// stream=false asks for a single sample. Docker still fills precpu_stats from the sample it
	// takes just before, so one request is enough to compute a delta.
	if err = d.do(http.MethodGet, "/containers/"+d.container+"/stats?stream=false", &s); err != nil {
		return 0, 0, 0, err
	}
	return cpuPercentOf(s), memUsedOf(s), s.MemoryStats.Limit, nil
}

func cpuPercentOf(s dockerStats) float64 {
	// No previous sample means there is nothing to compare against. Subtracting zero would still
	// yield positive deltas and a plausible-looking number, but it would be the average since the
	// container started rather than the current load.
	if s.PreCPUStats.SystemUsage == 0 {
		return 0
	}
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	if cpuDelta <= 0 || sysDelta <= 0 {
		return 0
	}
	cores := float64(s.CPUStats.OnlineCPUs)
	if cores == 0 {
		cores = 1
	}
	return cpuDelta / sysDelta * cores * 100
}

func memUsedOf(s dockerStats) uint64 {
	used := s.MemoryStats.Usage
	if cache := s.MemoryStats.Stats.InactiveFile; cache < used {
		used -= cache
	}
	return used
}

func (d dockerConfig) start() error {
	return d.do(http.MethodPost, "/containers/"+d.container+"/start", nil)
}

// stop asks Docker to shut the container down. Docker answers only once it is really down, and the
// server saves and backs up on the way out, so this call runs far longer than any status poll. With
// the poll budget the panel reported a failure while the stop was quietly succeeding.
func (d dockerConfig) stop() error {
	d.timeout = d.stopTimeout
	return d.do(http.MethodPost, "/containers/"+d.container+"/stop", nil)
}
