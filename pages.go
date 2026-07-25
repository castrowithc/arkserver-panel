package main

import (
	"net/http"
	"os"
	"strconv"
)

// tailLines is what the log viewer shows. Enough to cover a start-up sequence, short enough to
// render without thought.
const tailLines = 300

// pageChrome is what every page needs for the fixed frame around it: which entry of the navigation
// is current, whether the restart reminder is up, and whether a restart can be offered at all
// (without an RCON credential the panel must not offer one it cannot perform). SaveForm names the
// form the head's save button submits, so a page that has no form simply leaves it empty.
type pageChrome struct {
	Active     string
	Pending    bool
	CanRestart bool
	SaveForm   string
}

func newChrome(cfg config, active string) pageChrome {
	return pageChrome{Active: active, Pending: cfg.pending.get(), CanRestart: cfg.rcon.configured()}
}

type filePage struct {
	Chrome   pageChrome
	Files    []fileEntry
	Selected fileEntry
	Content  string
	Lines    int
	Flash    string
	Failed   bool
}

// withExistence marks the files that are actually present. The list files from the reference
// deployment may be absent, a game log only exists once it is switched on, so the picker shows them
// greyed rather than pretending they are there or hiding them and raising the question where they
// went.
func withExistence(entries []fileEntry) []fileEntry {
	for i, e := range entries {
		if _, err := os.Stat(e.Path); err == nil {
			entries[i].Exists = true
		}
	}
	return entries
}

func selected(entries []fileEntry, id string) fileEntry {
	if e, ok := findFile(entries, id); ok {
		return e
	}
	return entries[0]
}

func filesHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries := withExistence(configFiles(cfg))
		page := filePage{
			Chrome:   newChrome(cfg, "files"),
			Files:    entries,
			Selected: selected(entries, r.URL.Query().Get("f")),
		}
		switch {
		case r.URL.Query().Get("restarted") == "1":
			page.Flash = "Gespeichert, der Server startet neu."
		case r.URL.Query().Get("saved") == "1":
			page.Flash = "Gespeichert."
		}

		content, err := readTextFile(page.Selected.Path)
		switch {
		case err != nil:
			page.Flash, page.Failed = "Datei nicht lesbar: "+err.Error(), true
		case page.Selected.Secret:
			page.Content = maskSecrets(content)
		default:
			page.Content = content
		}
		render(w, "files.html", page)
	}
}

func saveFileHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !sameOrigin(r) {
			http.Error(w, "cross-site request rejected", http.StatusForbidden)
			return
		}
		entry, ok := findFile(configFiles(cfg), r.FormValue("f"))
		if !ok || !entry.Editable {
			http.Error(w, "not an editable file", http.StatusForbidden)
			return
		}
		if err := saveTextFile(entry.Path, r.FormValue("content")); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cfg.pending.set()

		// Opt-in, off unless the operator ticked the box: a save that takes the server down for
		// several minutes without being asked for would be a nasty surprise, particularly with
		// players connected.
		if r.FormValue("restart") == "1" && cfg.rcon.configured() {
			if err := restartServer(cfg.rcon); err != nil {
				// The file is written either way, so report the failed restart without pretending
				// the save did not happen.
				http.Error(w, "gespeichert, aber der Neustart schlug fehl: "+err.Error(), http.StatusBadGateway)
				return
			}
			cfg.pending.clear()
			http.Redirect(w, r, "/files?f="+entry.ID+"&restarted=1", http.StatusSeeOther)
			return
		}
		// Redirect rather than render, so a reload does not save again.
		http.Redirect(w, r, "/files?f="+entry.ID+"&saved=1", http.StatusSeeOther)
	}
}

type settingsPage struct {
	Chrome pageChrome
	Groups []settingGroup
	Fields int
	// Notices names a file the page could not read, so the greyed-out fields below have a reason.
	Notices []string
	Flash   string
	Failed  bool
}

// settingsFormID ties the form to the save button in the fixed head above it.
const settingsFormID = "settings-form"

func newSettingsPage(cfg config, sources map[string]*iniSource, groups []settingGroup) settingsPage {
	chrome := newChrome(cfg, "settings")
	chrome.SaveForm = settingsFormID
	page := settingsPage{Chrome: chrome, Groups: groups}
	for _, g := range groups {
		page.Fields += len(g.Rows)
	}
	for _, src := range sources {
		if src.err != nil {
			page.Notices = append(page.Notices, src.entry.Label+" nicht lesbar: "+src.err.Error())
		}
	}
	return page
}

func settingsHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sources := loadINISources(cfg)
		page := newSettingsPage(cfg, sources, buildGroups(sources))
		switch n := r.URL.Query().Get("saved"); {
		case r.URL.Query().Get("restarted") == "1":
			page.Flash = "Gespeichert, der Server startet neu."
		case n == "0":
			page.Flash = "Nichts zu speichern: die Werte stehen schon so in der Datei."
		case n != "":
			page.Flash = n + " Wert(e) gespeichert."
		}
		render(w, "settings.html", page)
	}
}

func saveSettingsHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !sameOrigin(r) {
			http.Error(w, "cross-site request rejected", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		sources := loadINISources(cfg)
		groups := buildGroups(sources)
		changed, ok := applyForm(sources, r.PostForm, groups)
		if !ok {
			// Nothing was written. The page comes back with the submitted values and the reasons,
			// so the operator can correct rather than retype.
			page := newSettingsPage(cfg, sources, groups)
			page.Flash, page.Failed = "Nichts gespeichert: bitte die markierten Felder pruefen.", true
			w.WriteHeader(http.StatusUnprocessableEntity)
			render(w, "settings.html", page)
			return
		}

		for _, src := range sources {
			if !src.dirty {
				continue
			}
			if err := saveTextFile(src.entry.Path, src.ini.render()); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if changed > 0 {
			cfg.pending.set()
		}

		if r.FormValue("restart") == "1" && cfg.rcon.configured() {
			if err := restartServer(cfg.rcon); err != nil {
				http.Error(w, "gespeichert, aber der Neustart schlug fehl: "+err.Error(), http.StatusBadGateway)
				return
			}
			cfg.pending.clear()
			http.Redirect(w, r, "/settings?restarted=1", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/settings?saved="+strconv.Itoa(changed), http.StatusSeeOther)
	}
}

func logsHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries := withExistence(logFiles(cfg))
		page := filePage{
			Chrome:   newChrome(cfg, "logs"),
			Files:    entries,
			Selected: selected(entries, r.URL.Query().Get("f")),
			Lines:    tailLines,
		}

		content, err := tailFile(page.Selected.Path, tailLines)
		if err != nil {
			page.Flash, page.Failed = "Log nicht lesbar: "+err.Error(), true
		}
		page.Content = content

		// The periodic refresh asks for the text alone, so it can swap the contents without
		// re-rendering the page around it.
		if r.URL.Query().Get("raw") == "1" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte(content))
			return
		}
		render(w, "logs.html", page)
	}
}
