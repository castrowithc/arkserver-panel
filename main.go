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

var templates = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

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
	rcon   rconConfig
	docker dockerConfig
	// pending carries the "edited, not yet restarted" marker across requests.
	pending *restartFlag
}

func loadConfig() config {
	return config{
		addr:    envOr("PANEL_ADDR", "127.0.0.1:8080"),
		user:    os.Getenv("PANEL_USER"),
		pass:    os.Getenv("PANEL_PASS"),
		dataDir: envOr("ARK_DATA_DIR", "/data"),
		envDir:  envOr("ARK_ENV_DIR", "/deploy"),
		pending: &restartFlag{},
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

func index(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		render(w, "index.html", gatherStatus(cfg))
	}
}

// statusFragment serves the same block the page embeds, so the poll replaces one region instead of
// reloading everything.
func statusFragment(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		render(w, "status", gatherStatus(cfg))
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
	app.HandleFunc("/files", filesHandler(cfg))
	app.HandleFunc("/files/save", saveFileHandler(cfg))
	app.HandleFunc("/logs", logsHandler(cfg))
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
