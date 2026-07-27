package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// tailLines is what the log viewer shows. Enough to cover a start-up sequence, short enough to
// render without thought.
const tailLines = 300

// pageChrome is what every page needs for the fixed frame around it: which entry of the navigation
// is current, whether the restart reminder is up, and whether a restart can be offered at all
// (without an RCON credential the panel must not offer one it cannot perform). SaveForm names the
// form the head's save button submits, so a page that has no form simply leaves it empty.
// Title is the page's own name, carried in the browser tab and in the fixed head, so a page can be
// referred to by the same name the documentation uses for it.
type pageChrome struct {
	Active     string
	Title      string
	Pending    bool
	CanRestart bool
	SaveForm   string
}

func newChrome(cfg config, active string) pageChrome {
	return pageChrome{
		Active:     active,
		Title:      navTitles[active],
		Pending:    cfg.pending.get(),
		CanRestart: cfg.rcon.configured(),
	}
}

// The navigation, grouped and named the way the reference screens are, so a page in the
// documentation and a page in the panel can be talked about as the same thing. Own marks an entry
// the reference has no counterpart for, rather than letting it pass as one of theirs.
type navEntry struct {
	Key   string
	Path  string
	Title string
	Own   bool
}

type navSection struct {
	Name    string
	Entries []navEntry
}

var navigation = []navSection{
	{Name: "Einstellungen", Entries: []navEntry{
		{Key: "status", Path: "/", Title: "Status"},
		{Key: "basis", Path: "/settings", Title: "Basiseinstellungen"},
		{Key: "dateien", Path: "/files", Title: "Konfigurationsdateien"},
		{Key: "logs", Path: "/logs", Title: "Logs"},
		{Key: "engine", Path: "/engine", Title: "Engine Einstellungen"},
	}},
	{Name: "Administration", Entries: []navEntry{
		{Key: "spielstand", Path: "/savegames", Title: "Spielstände", Own: true},
		{Key: "backup", Path: "/backups", Title: "Backup"},
		{Key: "deployment", Path: "/env", Title: "Deployment", Own: true},
	}},
}

var navTitles = func() map[string]string {
	titles := map[string]string{}
	for _, section := range navigation {
		for _, e := range section.Entries {
			titles[e.Key] = e.Title
		}
	}
	return titles
}()

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
			Chrome:   newChrome(cfg, "dateien"),
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

// settingsScreen is one of the two generated form pages. They differ in the part of the catalogue
// they show and in where they post; everything else about them is the same, so they share a handler
// and a template rather than being copied.
type settingsScreen struct {
	Key    string
	Path   string
	Save   string
	Engine bool
}

var (
	basisScreen  = settingsScreen{Key: "basis", Path: "/settings", Save: "/settings/save"}
	engineScreen = settingsScreen{Key: "engine", Path: "/engine", Save: "/engine/save", Engine: true}
)

type settingsPage struct {
	Chrome pageChrome
	// Action is where this page's form posts, which is what keeps a save on one screen from
	// reaching the fields of the other.
	Action string
	Groups []settingGroup
	Fields int
	// Notices names a file the page could not read, so the greyed-out fields below have a reason.
	Notices []string
	Flash   string
	Failed  bool
}

// settingsFormID ties the form to the save button in the fixed head above it.
const settingsFormID = "settings-form"

func newSettingsPage(cfg config, screen settingsScreen, sources map[string]*iniSource, groups []settingGroup) settingsPage {
	chrome := newChrome(cfg, screen.Key)
	chrome.SaveForm = settingsFormID
	page := settingsPage{Chrome: chrome, Action: screen.Save, Groups: groups}
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

func settingsHandler(cfg config, screen settingsScreen) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != screen.Path {
			http.NotFound(w, r)
			return
		}
		sources := loadINISources(cfg)
		groups := groupsForScreen(buildGroups(sources), screen.Engine)
		page := newSettingsPage(cfg, screen, sources, groups)
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

func saveSettingsHandler(cfg config, screen settingsScreen) http.HandlerFunc {
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
		// Narrowed to this screen before anything is applied, so a field of the other page is never
		// even considered, let alone written.
		groups := groupsForScreen(buildGroups(sources), screen.Engine)
		changed, ok := applyForm(sources, r.PostForm, groups)
		if !ok {
			// Nothing was written. The page comes back with the submitted values and the reasons,
			// so the operator can correct rather than retype.
			page := newSettingsPage(cfg, screen, sources, groups)
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
			http.Redirect(w, r, screen.Path+"?restarted=1", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, screen.Path+"?saved="+strconv.Itoa(changed), http.StatusSeeOther)
	}
}

type backupPage struct {
	Chrome  pageChrome
	Backups []backupEntry
	// CanRestore is false without Docker access. A restore has to stop the server and start it
	// again, and unpacking underneath a running server would look like it worked and change nothing.
	CanRestore bool
	Flash      string
	Failed     bool
}

func backupsHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := backupPage{Chrome: newChrome(cfg, "backup"), CanRestore: cfg.docker.configured()}
		entries, err := listBackups(cfg)
		if err != nil {
			page.Flash, page.Failed = "Sicherungen nicht lesbar: "+err.Error(), true
		}
		page.Backups = entries
		if n := r.URL.Query().Get("restored"); n != "" {
			page.Flash = n + " Datei(en) zurückgespielt, der Server läuft wieder."
		}
		render(w, "backups.html", page)
	}
}

// downloadBackup hands the archive out as a file. Only this makes it a backup in any real sense: on
// the volume it shares the fate of the world it is meant to protect.
func downloadBackupHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries, err := listBackups(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		entry, ok := findBackup(entries, r.URL.Query().Get("b"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(entry.path(cfg))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", entry.Name))
		http.ServeContent(w, r, entry.Name, entry.modTime, f)
	}
}

func restoreBackupHandler(cfg config) http.HandlerFunc {
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
		if !cfg.docker.configured() {
			http.Error(w, "ohne Docker-Zugriff kann das Panel den Server nicht stoppen", http.StatusForbidden)
			return
		}
		entries, err := listBackups(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		entry, ok := findBackup(entries, r.FormValue("b"))
		if !ok {
			http.Error(w, "unbekannte Sicherung", http.StatusNotFound)
			return
		}

		// Which save game is in force decides where the world goes back. With an alternate save
		// directory configured, the default one is not what the server reads.
		file, err := readInstanceFile(cfg)
		if err != nil {
			http.Error(w, "Instanzdatei nicht lesbar: "+err.Error(), http.StatusInternalServerError)
			return
		}
		saveDir, active := file.saveDirOrDefault(), resolveMap(cfg, file)

		// Refuse an archive from another map before taking the server down. The map is all an archive
		// reveals about where it came from, so this catches the clear case and not every case: two
		// save games of the same map cannot be told apart by their archives.
		world, err := archiveWorld(entry, entry.path(cfg))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if world != "" && active.Value != "" && !strings.EqualFold(world, active.Value+".ark") {
			http.Error(w, fmt.Sprintf(
				"Diese Sicherung enthält %s, geladen ist aber %s. Zurückspielen würde eine fremde Welt in den laufenden Spielstand schreiben.",
				world, active.Value), http.StatusConflict)
			return
		}

		// Stopping first is what makes the restore stick, and it has a second effect worth having:
		// with BACKUP_ON_STOP the shutdown writes one more backup of the current world, so the state
		// about to be overwritten is itself saved before it goes.
		if err := cfg.docker.stop(); err != nil {
			http.Error(w, "Server stoppen: "+err.Error(), http.StatusBadGateway)
			return
		}
		restored, restoreErr := restoreBackup(cfg, entry, saveDir)
		// Start again in either case: leaving the deployment down after a failed restore would turn
		// one problem into two.
		if err := cfg.docker.start(); err != nil {
			http.Error(w, "Server startet nicht mehr: "+err.Error(), http.StatusBadGateway)
			return
		}
		if restoreErr != nil {
			http.Error(w, "Zurückspielen fehlgeschlagen, der Server läuft wieder: "+restoreErr.Error(), http.StatusInternalServerError)
			return
		}
		// The files on disk are the archive's now, so a "saved but not yet in effect" marker from
		// before is meaningless.
		cfg.pending.clear()
		http.Redirect(w, r, "/backups?restored="+strconv.Itoa(restored), http.StatusSeeOther)
	}
}

type savegamePage struct {
	Chrome pageChrome
	Saves  []saveGame
	Maps   []mapChoice
	// Dirs are the save directories that already exist, so switching back to one is a pick rather
	// than a retyped name.
	Dirs []string
	// Origin is the map in force and where that answer comes from, which is the honest way to show a
	// value that may no longer agree with the .env.
	Origin  mapOrigin
	SaveDir string
	// CanSwitch is false without Docker access: the switch is a stop and a start, and nothing else
	// applies the change.
	CanSwitch bool
	Flash     string
	Failed    bool
}

func newSavegamePage(cfg config) savegamePage {
	page := savegamePage{
		Chrome:    newChrome(cfg, "spielstand"),
		Maps:      mapChoices(cfg),
		CanSwitch: cfg.docker.configured(),
	}
	file, err := readInstanceFile(cfg)
	if err != nil {
		page.Flash, page.Failed = "Instanzdatei nicht lesbar: "+err.Error(), true
		return page
	}
	page.SaveDir = file.saveDirOrDefault()
	page.Origin = resolveMap(cfg, file)

	saves, err := listSaveGames(cfg, page.SaveDir, page.Origin.Value)
	if err != nil {
		page.Flash, page.Failed = "Spielstände nicht lesbar: "+err.Error(), true
		return page
	}
	page.Saves = saves

	seen := map[string]bool{savedArksDir: true}
	page.Dirs = []string{savedArksDir}
	for _, s := range saves {
		if !seen[s.Dir] {
			seen[s.Dir] = true
			page.Dirs = append(page.Dirs, s.Dir)
		}
	}
	return page
}

func savegamesHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := newSavegamePage(cfg)
		if r.URL.Query().Get("switched") == "1" {
			page.Flash = "Umgeschaltet. Der Server lädt die Welt, das dauert einige Minuten; bis dahin ist er nicht erreichbar."
		}
		render(w, "savegames.html", page)
	}
}

// switchSavegameHandler applies one save game. The work is in applySaveGame; what happens here is
// only the reading of the form, because a wrong value must be refused before the server goes down.
func switchSavegameHandler(cfg config) http.HandlerFunc {
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
		if !cfg.docker.configured() {
			http.Error(w, "ohne Docker-Zugriff kann das Panel den Server nicht stoppen und starten", http.StatusForbidden)
			return
		}

		saveDir := r.FormValue("dir")
		// A new directory is typed rather than picked, and it wins over the picker so the form does
		// not silently ignore what was typed.
		if name := strings.TrimSpace(r.FormValue("newdir")); name != "" {
			saveDir = name
		}
		if saveDir == "" {
			saveDir = savedArksDir
		}

		if err := applySaveGame(cfg, r.FormValue("map"), saveDir); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		// The server has restarted, so a "saved but not yet in effect" marker from before is spent.
		cfg.pending.clear()
		http.Redirect(w, r, "/savegames?switched=1", http.StatusSeeOther)
	}
}

type envPage struct {
	Chrome pageChrome
	Values []envValue
	// Stale is true when the file has been edited since the container was built, so what the page
	// lists is not what the server is running with. Unknown without Docker access, hence Known.
	Stale  bool
	Known  bool
	Flash  string
	Failed bool
}

func envHandler(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := envPage{Chrome: newChrome(cfg, "deployment")}
		values, err := loadEnvValues(cfg)
		if err != nil {
			page.Flash, page.Failed = ".env nicht lesbar: "+err.Error(), true
			render(w, "env.html", page)
			return
		}
		page.Values = values

		if info, err := envModified(cfg); err == nil && cfg.docker.configured() {
			if st, err := cfg.docker.state(); err == nil && !st.Created.IsZero() {
				page.Known = true
				// Truncated to the second: one timestamp comes from Docker's JSON and the other from
				// the filesystem, so a sub-second difference between them says nothing.
				page.Stale = info.ModTime().Truncate(time.Second).After(st.Created.Truncate(time.Second))
			}
		}
		render(w, "env.html", page)
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
