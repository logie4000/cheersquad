package travel

import (
	"math"
	"testing"

	"github.com/logie4000/cheersquad/internal/geo"
)

func TestHaversine(t *testing.T) {
	h := Haversine{Speeds: map[Mode]float64{Walk: 2}, Detour: 1}
	a, b := geo.LatLon{}, geo.LatLon{Lat: 0.01}
	s, ok := h.Duration(a, b, Walk)
	if !ok || math.Abs(s-geo.Distance(a, b)/2) > 1e-9 {
		t.Fatalf("walk: %f %v", s, ok)
	}
	// Bike falls back to the default speed.
	s, _ = h.Duration(a, b, Bike)
	if math.Abs(s-geo.Distance(a, b)/DefaultSpeeds[Bike]) > 1e-9 {
		t.Fatalf("bike: %f", s)
	}
	if _, err := ParseMode("teleport"); err == nil {
		t.Fatal("expected error")
	}
}

type counting struct{ n int }

func (c *counting) Duration(a, b geo.LatLon, m Mode) (float64, bool) {
	c.n++
	return 1, true
}

func TestCached(t *testing.T) {
	inner := &counting{}
	c := &Cached{Inner: inner}
	for i := 0; i < 3; i++ {
		c.Duration(geo.LatLon{}, geo.LatLon{Lat: 1}, Walk)
	}
	c.Duration(geo.LatLon{}, geo.LatLon{Lat: 1}, Bike)
	if inner.n != 2 {
		t.Fatalf("inner called %d times, want 2", inner.n)
	}
}

func TestClosures(t *testing.T) {
	// Course runs north along lon 0. Spectator wants to go west to east.
	course := []geo.LatLon{{Lat: 0}, {Lat: 0.1}}
	inner := Haversine{Speeds: map[Mode]float64{Walk: 1}, Detour: 1}
	west, east := geo.LatLon{Lat: 0.05, Lon: -0.001}, geo.LatLon{Lat: 0.05, Lon: 0.001}

	blocked := Closures{Inner: inner, Course: course}
	if _, ok := blocked.Duration(west, east, Walk); ok {
		t.Fatal("crossing with no crossing points should be impossible")
	}

	cross := geo.LatLon{Lat: 0.06}
	c := Closures{Inner: inner, Course: course, Crossings: []geo.LatLon{cross}}
	s, ok := c.Duration(west, east, Walk)
	want := geo.Distance(west, cross) + geo.Distance(cross, east)
	if !ok || math.Abs(s-want) > 1e-6 {
		t.Fatalf("via crossing: %f %v, want %f", s, ok, want)
	}

	// Same side of the course is unaffected.
	west2 := geo.LatLon{Lat: 0.07, Lon: -0.001}
	s, ok = c.Duration(west, west2, Walk)
	if !ok || math.Abs(s-geo.Distance(west, west2)) > 1e-6 {
		t.Fatalf("same side: %f %v", s, ok)
	}

	// Moving between two spots on the course itself is not a crossing.
	s, ok = blocked.Duration(geo.LatLon{Lat: 0.02}, geo.LatLon{Lat: 0.08}, Walk)
	if !ok {
		t.Fatal("travel along the course should be allowed")
	}
}
