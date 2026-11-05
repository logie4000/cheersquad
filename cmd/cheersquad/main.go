// Command cheersquad serves the planner API under /api/ and the built web
// UI from a static directory.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/logie4000/cheersquad/internal/api"
	"github.com/logie4000/cheersquad/internal/site"
	"github.com/logie4000/cheersquad/internal/travel"
)

func main() {
	addr := flag.String("addr", envOr("ADDR", ":8080"), "listen address")
	static := flag.String("static", envOr("STATIC_DIR", "web/dist"), "directory of built UI files")
	gtfsDir := flag.String("gtfs-dir", envOr("GTFS_DIR", ""), "directory of GTFS feeds for subway routing (empty: straight-line transit estimate)")
	osrmWalk := flag.String("osrm-walk-url", envOr("OSRM_WALK_URL", ""), "OSRM server with a foot profile for walking times (empty: straight-line estimate)")
	siteURL := flag.String("site-url", envOr("SITE_URL", ""), "public base URL for the canonical link, social cards and sitemap, e.g. https://www.example.org/cheersquad/ (empty: from the request's Host and X-Forwarded-* headers)")
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
	// The key comes from the environment only, never a flag, so it stays
	// out of process listings.
	if key := os.Getenv("ORS_API_KEY"); key != "" {
		ors := &travel.ORS{Key: key}
		if path := envOr("ORS_CACHE_FILE", ""); path != "" {
			// Saved answers outlive restarts, so repeat plans of a course
			// cost no quota. Without the file they're kept in memory only.
			if err := ors.OpenCache(path); err != nil {
				log.Printf("ors cache: %v; street routes are remembered in memory only", err)
			}
		}
		cfg.Streets = ors
		log.Printf("street routes for plan legs from openrouteservice.org")
	}

	mux := http.NewServeMux()
	mux.Handle("/api/", noIndex(api.Handler(cfg)))
	mux.Handle("/", site.Handler(site.Config{Dir: *static, URL: *siteURL}))

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

// noIndex keeps API answers (race GPX files, errors) out of search results.
func noIndex(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex")
		h.ServeHTTP(w, r)
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
