package main

import (
	"log"
	"sync"
	"time"

	"github.com/logie4000/cheersquad/internal/api"
	"github.com/logie4000/cheersquad/internal/gtfs"
	"github.com/logie4000/cheersquad/internal/travel"
)

// raceDays maps each race day to the days of the week whose service it
// uses.
var raceDays = map[api.RaceDay][]time.Weekday{
	api.Sunday:   {time.Sunday},
	api.Saturday: {time.Saturday},
	api.Weekday:  gtfs.WeekdayDays,
}

// loadNetworks loads every GTFS feed under dir once per race day, in
// parallel. Errors are logged and the server keeps going with whatever
// loaded: transit mode falls back to the straight-line estimate where no
// network covers a course.
func loadNetworks(dir string) map[api.RaceDay][]travel.Network {
	if dir == "" {
		log.Printf("no GTFS_DIR: transit mode uses the straight-line estimate")
		return nil
	}
	out := map[api.RaceDay][]travel.Network{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for day, weekdays := range raceDays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nets, err := gtfs.LoadDir(dir, gtfs.Options{Days: weekdays})
			if err != nil {
				log.Printf("gtfs: loading %s for %s: %v", dir, day, err)
			}
			var list []travel.Network
			for _, n := range nets {
				log.Printf("subway (%s): %s, %d stations", day, n.Name(), len(n.Stations()))
				list = append(list, n)
			}
			mu.Lock()
			out[day] = list
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}
