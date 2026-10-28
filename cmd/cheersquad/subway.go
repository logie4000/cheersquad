package main

import (
	"log"

	"github.com/logie4000/cheersquad/internal/gtfs"
	"github.com/logie4000/cheersquad/internal/travel"
)

// loadNetworks loads every GTFS feed under dir. Errors are logged and the
// server keeps going with whatever loaded: transit mode falls back to the
// straight-line estimate where no network covers a course.
func loadNetworks(dir string) []travel.Network {
	if dir == "" {
		log.Printf("no GTFS_DIR: transit mode uses the straight-line estimate")
		return nil
	}
	nets, err := gtfs.LoadDir(dir, gtfs.Options{})
	if err != nil {
		log.Printf("gtfs: loading %s: %v", dir, err)
	}
	var out []travel.Network
	for _, n := range nets {
		log.Printf("subway: %s, %d stations", n.Name(), len(n.Stations()))
		out = append(out, n)
	}
	return out
}
