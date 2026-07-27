// Command arkserver-panel is the self-hosted management panel for the hardened ARK: Survival
// Evolved server. v1 skeleton: an HTTP server behind Basic Auth, LAN-only by default.
package main

import (
	"crypto/subtle"
	"embed"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

//go:embed templates/*.html
var templatesFS embed.FS

// version is baked in at build time from the git tag the image is also tagged with
// (-X main.version=...). It is the one answer that cannot drift: a version file in the repo has to
// be kept in step by hand, and the pin in the deployment's .env says what should run, which is not
// the same as what does run until the container has actually been recreated.
var version = "dev"

// The navigation is one fixed structure for the whole panel, so the templates read it from here
// rather than every page carrying a copy of it through its own struct.
var templates = template.Must(template.New("").
	Funcs(template.FuncMap{
		"navigation": func() []navSection { return navigation },
		// envKind lets the .env form pick a control per key without the typing being duplicated in
		// the template, where it could drift away from what the writer accepts.
		"envKind": envKindName,
	}).
	ParseFS(templatesFS, "templates/*.html"))

type config struct {
	addr string
	user string
	pass string
	// dataDir is the server volume, mounted read-write: the app manifest and the INIs live here.
	dataDir string
	// envDir holds the deployment's .env, shown read-only. Mounted as a directory rather than as
	// the single file: a single-file mount pins an inode, so a host-side editor that replaces the
	// file on save would leave the panel showing the old content forever.
	envDir string
	// publicHost is how the server is reached from outside, set by the operator. The panel does not
	// look it up: it is deliberately bound to the local network, and a service in that position must
	// not go and ask an outside party for its own address.
	publicHost string
	// lanHost is the host's address inside the local network. Normally taken from the address the
	// browser used, but that fails in the common case: the panel is published on the loopback only,
	// so it is usually opened as localhost, and localhost says nothing about the network. Then this
	// is the only way the page can name it.
	lanHost string
	// envWrite is the .env mounted a second time, as a single file and writable. Empty means the
	// deployment did not wire that up and the file stays read-only. The directory above stays
	// read-only either way, because writable it would include the compose file.
	envWrite string
	rcon     rconConfig
	docker   dockerConfig
	// pending carries the "edited, not yet restarted" marker across requests.
	pending *restartFlag
	// archives caches what each backup archive says about itself, which costs a decompression to
	// find out and never changes afterwards.
	archives *archiveCache
}

func loadConfig() config {
	return config{
		addr:       envOr("PANEL_ADDR", "127.0.0.1:8080"),
		user:       os.Getenv("PANEL_USER"),
		pass:       os.Getenv("PANEL_PASS"),
		dataDir:    envOr("ARK_DATA_DIR", "/data"),
		envDir:     envOr("ARK_ENV_DIR", "/deploy"),
		publicHost: os.Getenv("ARK_PUBLIC_HOST"),
		lanHost:    os.Getenv("ARK_LAN_HOST"),
		envWrite:   os.Getenv("ARK_ENV_WRITE"),
		pending:    &restartFlag{},
		archives:   &archiveCache{},
		rcon: rconConfig{
			// Default to the compose service alias rather than the container name, which carries
			// COMPOSE_PROJECT_NAME and therefore changes with the deployment.
			addr: envOr("ARK_RCON_ADDR", "ark:27020"),
			pass: os.Getenv("ARK_ADMIN_PASSWORD"),
			// A connect either succeeds at once or the server is unreachable. A status poll gives
			// up quickly, because during a world reload the port accepts connections long before
			// it answers on them. A restart gets a wide budget for SaveWorld.
			dialTimeout:   5 * time.Second,
			statusBudget:  5 * time.Second,
			commandBudget: 120 * time.Second,
		},
		docker: dockerConfig{
			// Empty by default: without the socket proxy the panel simply hides what needs it.
			host: os.Getenv("ARK_DOCKER_HOST"),
			// The Docker API addresses a container by name or id, not by the compose service alias
			// that RCON connects to: "ark" resolves in DNS but is not the container's name.
			container: envOr("ARK_CONTAINER", "ark_server"),
			// The stats call samples twice internally, so it is never instant.
			timeout: 10 * time.Second,
			// The stop is the one call that waits for the server itself, not just for Docker, so it
			// gets the budget the compose file grants the shutdown (stop_grace_period).
			stopTimeout: 5 * time.Minute,
		},
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// basicAuth gates next behind one admin credential. Both comparisons run unconditionally and in
// constant time so neither the username nor the password leaks via timing. LAN-only per the auth
// decision: over plain HTTP the credential is only base64-encoded, so never expose without HTTPS.
func basicAuth(next http.Handler, user, pass string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		userOK := subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(p), []byte(pass)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="arkserver-panel"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// indexPage wraps the monitor's own data in the frame every page carries. The status block stays a
// value of its own, because the poll below re-renders exactly that block and nothing around it.
type indexPage struct {
	Chrome pageChrome
	Status serverStatus
}

func index(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		render(w, "index.html", indexPage{Chrome: newChrome(cfg, "status"), Status: gatherStatus(cfg, r.Host)})
	}
}

// statusFragment serves the same block the page embeds, so the poll replaces one region instead of
// reloading everything.
func statusFragment(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		render(w, "status", gatherStatus(cfg, r.Host))
	}
}

func render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// action performs one lifecycle command and returns to the page. POST only, and same-origin only:
// a browser attaches cached Basic-Auth credentials to a cross-site form post, so without the check
// a page in another tab could stop the server.
func action(run func() error) http.HandlerFunc {
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
		if err := run(); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// sameOrigin trusts the browser's own account of where the request came from, and falls back to
// Origin for browsers that do not send Sec-Fetch-Site. A request with neither header is not from a
// browser form, so Basic Auth alone already gates it.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		break
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

func newRouter(cfg config) http.Handler {
	app := http.NewServeMux()
	app.HandleFunc("/", index(cfg))
	app.HandleFunc("/status", statusFragment(cfg))
	// Two generated form pages, named after the reference screens they correspond to. They share a
	// handler and post to their own path, so a save on one never reaches the fields of the other.
	app.HandleFunc(basisScreen.Path, settingsHandler(cfg, basisScreen))
	app.HandleFunc(basisScreen.Save, saveSettingsHandler(cfg, basisScreen))
	app.HandleFunc(engineScreen.Path, settingsHandler(cfg, engineScreen))
	app.HandleFunc(engineScreen.Save, saveSettingsHandler(cfg, engineScreen))
	app.HandleFunc("/files", filesHandler(cfg))
	app.HandleFunc("/files/save", saveFileHandler(cfg))
	app.HandleFunc("/logs", logsHandler(cfg))
	app.HandleFunc("/env", envHandler(cfg))
	app.HandleFunc("/env/save", saveEnvHandler(cfg))
	app.HandleFunc("/savegames", savegamesHandler(cfg))
	app.HandleFunc("/savegames/switch", switchSavegameHandler(cfg))
	app.HandleFunc("/backups", backupsHandler(cfg))
	app.HandleFunc("/backups/download", downloadBackupHandler(cfg))
	app.HandleFunc("/backups/restore", restoreBackupHandler(cfg))
	app.HandleFunc("/restart", action(func() error {
		if err := restartServer(cfg.rcon); err != nil {
			return err
		}
		// The edits are in effect from here on, so the reminder has done its job.
		cfg.pending.clear()
		return nil
	}))
	app.HandleFunc("/start", action(cfg.docker.start))
	app.HandleFunc("/stop", action(cfg.docker.stop))

	mux := http.NewServeMux()
	// /healthz stays open for the container probe; everything else sits behind Basic Auth.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("/", basicAuth(app, cfg.user, cfg.pass))
	return mux
}

func main() {
	cfg := loadConfig()
	if cfg.user == "" || cfg.pass == "" {
		log.Fatal("PANEL_USER and PANEL_PASS must be set")
	}
	// Not fatal: without the RCON credential the panel still serves everything that does not need
	// the game server, so it degrades instead of refusing to start.
	if !cfg.rcon.configured() {
		log.Print("ARK_ADMIN_PASSWORD is unset: the player list and the restart action stay unavailable")
	}
	log.Printf("arkserver-panel listening on %s", cfg.addr)
	if err := http.ListenAndServe(cfg.addr, newRouter(cfg)); err != nil {
		log.Fatal(err)
	}
}
