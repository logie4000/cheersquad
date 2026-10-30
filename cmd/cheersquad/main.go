// Command cheersquad serves the planner API under /api/ and the built web
// UI from a static directory.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/logie4000/cheersquad/internal/api"
	"github.com/logie4000/cheersquad/internal/travel"
)

func main() {
	addr := flag.String("addr", envOr("ADDR", ":8080"), "listen address")
	static := flag.String("static", envOr("STATIC_DIR", "web/dist"), "directory of built UI files")
	gtfsDir := flag.String("gtfs-dir", envOr("GTFS_DIR", ""), "directory of GTFS feeds for subway routing (empty: straight-line transit estimate)")
	osrmWalk := flag.String("osrm-walk-url", envOr("OSRM_WALK_URL", ""), "OSRM server with a foot profile for walking times (empty: straight-line estimate)")
	healthcheck := flag.Bool("healthcheck", false, "probe /api/healthz on addr and exit (for Docker HEALTHCHECK)")
	flag.Parse()

	if *healthcheck {
		os.Exit(probe(*addr))
	}

	nets := loadNetworks(*gtfsDir)
	cfg := api.Config{Networks: nets[api.Sunday], DayNetworks: nets}
	if *osrmWalk != "" {
		cfg.Walk = &travel.OSRM{BaseURL: *osrmWalk}
		log.Printf("walking times from OSRM at %s", *osrmWalk)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", api.Handler(cfg))
	mux.Handle("/", spa(*static))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	log.Printf("cheersquad listening on %s, UI from %s", *addr, *static)
	log.Fatal(srv.ListenAndServe())
}

// spa serves files from dir, falling back to index.html so client-side
// routes work on reload.
func spa(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(filepath.Join(dir, filepath.Clean("/"+r.URL.Path))); err != nil {
			index := filepath.Join(dir, "index.html")
			if _, err := os.Stat(index); err != nil {
				http.Error(w, "CheerSquad UI not built yet; the API is at /api/", http.StatusNotFound)
				return
			}
			http.ServeFile(w, r, index)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func probe(addr string) int {
	host := addr
	if len(host) > 0 && host[0] == ':' {
		host = "127.0.0.1" + host
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + host + "/api/healthz")
	if err != nil || resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
