// Package travel estimates how long the spectator takes to get between two
// places. Every provider sits behind the Provider interface so the planner
// never knows whether it is talking to the offline stub or a routing API.
package travel

import (
	"fmt"
	"math"
	"sync"

	"github.com/logie4000/cheersquad/internal/geo"
)

// Mode is how the spectator gets around.
type Mode string

const (
	Walk    Mode = "walk"
	Bike    Mode = "bike"
	Transit Mode = "transit"
	Drive   Mode = "drive"
)

// DefaultSpeeds are door-to-door speeds in meters per second on race day,
// including crowds, parking, waiting for trains, and so on.
var DefaultSpeeds = map[Mode]float64{
	Walk:    1.3, // ~4.7 km/h through crowds
	Bike:    4.0, // ~14 km/h, walking the bike near the course
	Transit: 4.5, // ~16 km/h including waits
	Drive:   5.5, // ~20 km/h with closures and parking
}

// Provider returns travel time in seconds from a to b, and false if b cannot
// be reached from a at all.
type Provider interface {
	Duration(a, b geo.LatLon, m Mode) (seconds float64, ok bool)
}

// Haversine is the offline stub: straight-line distance times a detour
// factor, divided by the mode's speed. It never makes network calls.
type Haversine struct {
	// Speeds overrides DefaultSpeeds per mode.
	Speeds map[Mode]float64
	// Detour multiplies straight-line distance to approximate real paths.
	// Zero means 1.3.
	Detour float64
}

// Speed returns the speed used for mode m in meters per second.
func (h Haversine) Speed(m Mode) float64 {
	if s, ok := h.Speeds[m]; ok {
		return s
	}
	return DefaultSpeeds[m]
}

func (h Haversine) Duration(a, b geo.LatLon, m Mode) (float64, bool) {
	s := h.Speed(m)
	if s <= 0 {
		return 0, false
	}
	return geo.Distance(a, b) * h.detour() / s, true
}

// ParseMode validates a mode string.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case Walk, Bike, Transit, Drive:
		return m, nil
	}
	return "", fmt.Errorf("travel: unknown mode %q", s)
}

// Cached memoizes another provider. Routing APIs are slow and rate limited,
// and the planner asks about the same pairs repeatedly.
type Cached struct {
	Inner Provider
	mu    sync.Mutex
	memo  map[cacheKey]cacheVal
}

type cacheKey struct {
	a, b geo.LatLon
	m    Mode
}

type cacheVal struct {
	s  float64
	ok bool
}

func (c *Cached) Duration(a, b geo.LatLon, m Mode) (float64, bool) {
	k := cacheKey{a, b, m}
	c.mu.Lock()
	if v, hit := c.memo[k]; hit {
		c.mu.Unlock()
		return v.s, v.ok
	}
	c.mu.Unlock()
	s, ok := c.Inner.Duration(a, b, m)
	c.mu.Lock()
	if c.memo == nil {
		c.memo = map[cacheKey]cacheVal{}
	}
	c.memo[k] = cacheVal{s, ok}
	c.mu.Unlock()
	return s, ok
}

// Closures blocks travel across the course while the race is on, except at
// marked crossing points. A leg whose straight line crosses the course is
// rerouted through the best crossing point, or is impossible if there is
// none.
type Closures struct {
	Inner     Provider
	Course    []geo.LatLon
	Crossings []geo.LatLon
	// Tolerance ignores crossings within this many meters of either end of
	// a leg, since viewing spots sit on the course itself. Zero means 25.
	Tolerance float64
}

func (c Closures) Duration(a, b geo.LatLon, m Mode) (float64, bool) {
	_, _, s, ok := c.route(a, b, m)
	return s, ok
}

// route returns the leg's duration and, if it must detour, the crossing
// point x it goes through (via is true).
func (c Closures) route(a, b geo.LatLon, m Mode) (x geo.LatLon, via bool, s float64, ok bool) {
	if !c.crosses(a, b) {
		s, ok = c.Inner.Duration(a, b, m)
		return a, false, s, ok
	}
	best, found := math.Inf(1), false
	for _, p := range c.Crossings {
		if c.crosses(a, p) || c.crosses(p, b) {
			continue
		}
		s1, ok1 := c.Inner.Duration(a, p, m)
		s2, ok2 := c.Inner.Duration(p, b, m)
		if ok1 && ok2 && s1+s2 < best {
			best, found, x = s1+s2, true, p
		}
	}
	return x, true, best, found
}

// crosses reports whether the straight line a-b crosses the course away
// from its endpoints.
func (c Closures) crosses(a, b geo.LatLon) bool {
	tol := c.Tolerance
	if tol == 0 {
		tol = 25
	}
	l := geo.Distance(a, b)
	for i := 0; i+1 < len(c.Course); i++ {
		t, ok := geo.SegmentIntersection(a, b, c.Course[i], c.Course[i+1])
		if ok && t*l > tol && (1-t)*l > tol {
			return true
		}
	}
	return false
}
