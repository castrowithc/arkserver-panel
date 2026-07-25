// Command arkserver-panel is the self-hosted management panel for the hardened ARK: Survival
// Evolved server. v1 skeleton: an HTTP server behind Basic Auth, LAN-only by default.
package main

import (
	"crypto/subtle"
	"embed"
	"html/template"
	"log"
	"net/http"
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
	// dataDir is the server volume, mounted read-write: the app manifest is read from it now, the
	// INIs get edited through it in a later phase.
	dataDir string
	rcon    rconConfig
}

func loadConfig() config {
	return config{
		addr:    envOr("PANEL_ADDR", "127.0.0.1:8080"),
		user:    os.Getenv("PANEL_USER"),
		pass:    os.Getenv("PANEL_PASS"),
		dataDir: envOr("ARK_DATA_DIR", "/data"),
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

func index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if err := templates.ExecuteTemplate(w, "index.html", nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func newRouter(cfg config) http.Handler {
	mux := http.NewServeMux()
	// /healthz stays open for the container probe; everything else sits behind Basic Auth.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.Handle("/", basicAuth(http.HandlerFunc(index), cfg.user, cfg.pass))
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
