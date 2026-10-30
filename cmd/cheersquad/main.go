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
	osrmWalk := flag.String("osrm-walk-url", envOr("OSRM_WALK_URL", ""), "OSRM server with a foot profile for walking times and routes (empty: straight-line estimate)")
	osrmBike := flag.String("osrm-bike-url", envOr("OSRM_BIKE_URL", ""), "OSRM server with a bicycle profile for biking times and routes (empty: straight-line estimate)")
	osrmDrive := flag.String("osrm-drive-url", envOr("OSRM_DRIVE_URL", ""), "OSRM server with a car profile for driving times and routes (empty: straight-line estimate)")
	healthcheck := flag.Bool("healthcheck", false, "probe /api/healthz on addr and exit (for Docker HEALTHCHECK)")
	flag.Parse()

	if *healthcheck {
		os.Exit(probe(*addr))
	}

	nets := loadNetworks(*gtfsDir)
	cfg := api.Config{Networks: nets[api.Sunday], DayNetworks: nets}
	// Each router gets a second client that leaves ferries out, for
	// spectators who ask to avoid them. It has its own cache.
	cfg.NoFerry = map[travel.Mode]travel.Provider{}
	routed := func(name string, mk func() *travel.OSRM) *travel.OSRM {
		o, noFerry := mk(), mk()
		noFerry.Exclude = "ferry"
		cfg.NoFerry[o.Mode] = noFerry
		log.Printf("%s routes from OSRM at %s", name, o.BaseURL)
		return o
	}
	if *osrmWalk != "" {
		cfg.Walk = routed("walking", func() *travel.OSRM {
			return &travel.OSRM{BaseURL: *osrmWalk, Mode: travel.Walk}
		})
	}
	if *osrmBike != "" {
		// Crowds near the course slow bikes, and the bike has to be locked
		// up and left a short walk from each spot.
		cfg.Bike = routed("biking", func() *travel.OSRM {
			return &travel.OSRM{BaseURL: *osrmBike, Mode: travel.Bike, Factor: 1.2, Overhead: 120}
		})
	}
	if *osrmDrive != "" {
		// OSRM assumes empty streets; race-day traffic is slower, and every
		// stop means finding parking and walking to the course.
		cfg.Drive = routed("driving", func() *travel.OSRM {
			return &travel.OSRM{BaseURL: *osrmDrive, Mode: travel.Drive, Factor: 1.5, Overhead: 600}
		})
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
