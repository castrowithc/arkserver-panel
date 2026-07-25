package main

import "fmt"

// serverStatus is everything the monitor shows, already shaped for the template: no arithmetic and
// no error handling in the HTML.
type serverStatus struct {
	Lifecycle string // one of: laeuft, gestoppt, startet, unbekannt
	Label     string // the same, in words, for the page
	Health    string

	CPUPercent string
	MemUsed    string
	MemLimit   string
	MemPercent float64

	Players      []string
	PlayerCount  int
	PlayersKnown bool

	Build string

	// CanStartStop tells the page whether to offer the container actions at all, rather than
	// showing buttons that are guaranteed to fail.
	CanStartStop bool
	// PendingRestart marks config edits that are saved but not yet in effect. It rides along on the
	// monitor so the reminder is visible from wherever the operator happens to be looking.
	PendingRestart bool
	Notices        []string
}

// gatherStatus asks every source independently and degrades per source. A server that is down must
// still render a page that says so, so nothing here turns a failed lookup into a failed request.
func gatherStatus(cfg config) serverStatus {
	st := serverStatus{
		Lifecycle: "unbekannt", Label: "unbekannt",
		CPUPercent: "-", MemUsed: "-", MemLimit: "-", Build: "-",
		PendingRestart: cfg.pending.get(),
	}

	dockerOK := cfg.docker.configured()
	st.CanStartStop = dockerOK
	running := false
	if dockerOK {
		if state, err := cfg.docker.state(); err != nil {
			st.Notices = append(st.Notices, "Container-Status nicht lesbar: "+err.Error())
			st.CanStartStop = false
		} else {
			running = state.State.Running
			if state.State.Health != nil {
				st.Health = state.State.Health.Status
			}
			if running {
				if cpu, used, limit, err := cfg.docker.usage(); err != nil {
					st.Notices = append(st.Notices, "Auslastung nicht lesbar: "+err.Error())
				} else {
					st.CPUPercent = fmt.Sprintf("%.1f", cpu)
					st.MemUsed, st.MemLimit = formatGB(used), formatGB(limit)
					if limit > 0 {
						st.MemPercent = float64(used) / float64(limit) * 100
					}
				}
			}
		}
	} else {
		st.Notices = append(st.Notices, "Kein Docker-Zugriff konfiguriert: Auslastung sowie Start und Stopp sind nicht verfügbar.")
	}

	rconOK, rconConfigured := false, cfg.rcon.configured()
	if rconConfigured {
		if players, err := listPlayers(cfg.rcon); err == nil {
			rconOK = true
			st.Players, st.PlayerCount, st.PlayersKnown = players, len(players), true
		}
	} else {
		st.Notices = append(st.Notices, "Kein RCON-Passwort konfiguriert: Spielerliste und Neustart sind nicht verfügbar.")
	}

	if build, err := installedBuild(cfg.dataDir); err == nil {
		st.Build = build
	} else {
		st.Notices = append(st.Notices, "Version nicht lesbar: "+err.Error())
	}

	st.Lifecycle, st.Label = lifecycleOf(dockerOK, running, rconConfigured, rconOK)
	return st
}

// lifecycleOf combines the two signals. Neither alone is enough: RCON goes quiet both when the
// server is stopped and while it reloads its world, and Docker only knows about the container, not
// about the game process inside it.
func lifecycleOf(dockerKnown, running, rconConfigured, rconOK bool) (string, string) {
	switch {
	case rconOK:
		return "laeuft", "läuft"
	case dockerKnown && !running:
		return "gestoppt", "gestoppt"
	case dockerKnown && running && rconConfigured:
		// The container is up and the game was asked but stayed silent: it is booting or reloading
		// its world, which takes about four to five minutes. Deliberately not an error.
		return "startet", "startet, Welt wird geladen"
	case dockerKnown && running:
		// Nobody asked the game, so claiming it is loading would be a guess.
		return "unbekannt", "Container läuft, Spielstatus unbekannt"
	default:
		return "unbekannt", "nicht erreichbar"
	}
}

func formatGB(b uint64) string {
	return fmt.Sprintf("%.1f", float64(b)/(1024*1024*1024))
}
