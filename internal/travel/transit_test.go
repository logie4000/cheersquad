package travel

import (
	"math"
	"math/rand"
	"testing"

	"github.com/logie4000/cheersquad/internal/geo"
)

// lineNet is three stations due east along the equator, 0.03 degrees
// (about 3.3 km) apart. Line A runs 0-1, line B runs 1-2. Each ride hop
// takes 300 s and entering the system costs 120 s that no Ride includes.
type lineNet struct{}

var lineStations = []Station{
	{ID: "s0", Name: "West", Pos: geo.LatLon{Lon: 0}},
	{ID: "s1", Name: "Middle", Pos: geo.LatLon{Lon: 0.03}},
	{ID: "s2", Name: "East", Pos: geo.LatLon{Lon: 0.06}},
}

func (lineNet) Name() string           { return "Test Metro" }
func (lineNet) Stations() []Station    { return lineStations }
func (lineNet) MaxSpeed() float64      { return 12 }
func (lineNet) Covers(geo.LatLon) bool { return true }

func (lineNet) Time(i, j int) (float64, bool) {
	if i == j {
		return 0, true
	}
	return 120 + 300*math.Abs(float64(i-j)), true
}

func (lineNet) Rides(i, j int) []Ride {
	var rs []Ride
	step := 1
	if j < i {
		step = -1
	}
	for k := i; k != j; k += step {
		line := "A"
		if k+step == 2 || k == 2 {
			line = "B"
		}
		if n := len(rs); n > 0 && rs[n-1].Line == line {
			rs[n-1].To = k + step
			rs[n-1].Seconds += 300
			continue
		}
		rs = append(rs, Ride{Line: line, From: k, To: k + step, Seconds: 300})
	}
	return rs
}

var unitWalk = Haversine{Speeds: map[Mode]float64{Walk: 1}, Detour: 1}

func sumSteps(steps []Step) float64 {
	s := 0.0
	for _, st := range steps {
		s += st.Seconds
	}
	return s
}

func kinds(steps []Step) []StepKind {
	var ks []StepKind
	for _, s := range steps {
		ks = append(ks, s.Kind)
	}
	return ks
}

func TestSubwayShortHopWalks(t *testing.T) {
	s := &Subway{Net: lineNet{}, Walk: unitWalk}
	a, b := geo.LatLon{Lon: 0.001}, geo.LatLon{Lon: 0.004}
	d, ok := s.Duration(a, b, Transit)
	if want := geo.Distance(a, b); !ok || math.Abs(d-want) > 1e-6 {
		t.Fatalf("short hop: %f %v, want walking %f", d, ok, want)
	}
	steps, ok := s.Steps(a, b, Transit)
	if !ok || len(steps) != 1 || steps[0].Kind != StepWalk || steps[0].Seconds != d {
		t.Fatalf("short hop steps: %+v", steps)
	}
}

func TestSubwayLongHopRides(t *testing.T) {
	s := &Subway{Net: lineNet{}, Walk: unitWalk}
	a, b := geo.LatLon{Lat: 0.001}, geo.LatLon{Lat: 0.001, Lon: 0.06}
	d, ok := s.Duration(a, b, Transit)
	walkA := geo.Distance(a, lineStations[0].Pos)
	walkB := geo.Distance(lineStations[2].Pos, b)
	if want := walkA + 720 + walkB; !ok || math.Abs(d-want) > 1e-6 {
		t.Fatalf("long hop: %f %v, want %f", d, ok, want)
	}
	steps, ok := s.Steps(a, b, Transit)
	if !ok {
		t.Fatal("no steps")
	}
	want := []StepKind{StepWalk, StepSubway, StepSubway, StepWalk}
	if got := kinds(steps); len(got) != len(want) {
		t.Fatalf("kinds %v, want %v", got, want)
	}
	if steps[1].Line != "A" || steps[1].From != "West" || steps[1].To != "Middle" || steps[1].Seconds != 420 {
		t.Fatalf("first ride should carry the entry overhead: %+v", steps[1])
	}
	if steps[2].Line != "B" || steps[2].To != "East" || steps[2].ToPos != lineStations[2].Pos || steps[2].Seconds != 300 {
		t.Fatalf("second ride: %+v", steps[2])
	}
	if math.Abs(sumSteps(steps)-d) > 1e-9 {
		t.Fatalf("steps sum %f, duration %f", sumSteps(steps), d)
	}
	// Other modes go to Walk unchanged.
	if bd, _ := s.Duration(a, b, Bike); math.Abs(bd-geo.Distance(a, b)/DefaultSpeeds[Bike]) > 1e-6 {
		t.Fatalf("bike delegated: %f", bd)
	}
}

func TestSubwayGoesUnderClosedCourse(t *testing.T) {
	// The course runs north along lon 0.015, between stations 0 and 1.
	course := []geo.LatLon{{Lat: -0.1, Lon: 0.015}, {Lat: 0.1, Lon: 0.015}}
	walk := Closures{Inner: unitWalk, Course: course}
	a, b := geo.LatLon{Lat: 0.001, Lon: 0.005}, geo.LatLon{Lat: 0.001, Lon: 0.025}
	if _, ok := walk.Duration(a, b, Walk); ok {
		t.Fatal("walking across the closed course should be impossible")
	}
	s := &Subway{Net: lineNet{}, Walk: walk}
	d, ok := s.Duration(a, b, Transit)
	if !ok {
		t.Fatal("the subway should pass under the course")
	}
	steps, _ := s.Steps(a, b, Transit)
	if got := kinds(steps); len(got) != 3 || got[0] != StepWalk || got[1] != StepSubway || got[2] != StepWalk {
		t.Fatalf("kinds %v", got)
	}
	if math.Abs(sumSteps(steps)-d) > 1e-9 {
		t.Fatalf("steps sum %f, duration %f", sumSteps(steps), d)
	}
}

func TestClosuresSteps(t *testing.T) {
	course := []geo.LatLon{{Lat: 0}, {Lat: 0.1}}
	cross := geo.LatLon{Lat: 0.06}
	c := Closures{Inner: unitWalk, Course: course, Crossings: []geo.LatLon{cross}}
	west, east := geo.LatLon{Lat: 0.05, Lon: -0.001}, geo.LatLon{Lat: 0.05, Lon: 0.001}
	steps, ok := c.Steps(west, east, Walk)
	d, _ := c.Duration(west, east, Walk)
	if !ok || len(steps) != 2 || steps[0].ToPos != cross || steps[1].FromPos != cross {
		t.Fatalf("steps via crossing: %+v", steps)
	}
	if math.Abs(sumSteps(steps)-d) > 1e-9 {
		t.Fatalf("steps sum %f, duration %f", sumSteps(steps), d)
	}
	cached := &Cached{Inner: c}
	if cs, _ := cached.Steps(west, east, Walk); len(cs) != 2 {
		t.Fatalf("cached should pass through: %+v", cs)
	}
	if cached.MaxSpeed(Walk) != 1 {
		t.Fatalf("cached max speed %f", cached.MaxSpeed(Walk))
	}
	if (&Cached{Inner: &counting{}}).MaxSpeed(Walk) != 0 {
		t.Fatal("unknown inner bound should be 0")
	}
}

// TestSpeedBounds checks MaxSpeed really bounds straight-line speed, and
// that steps sum to the duration, over random pairs.
func TestSpeedBounds(t *testing.T) {
	course := []geo.LatLon{{Lat: -0.1, Lon: 0.015}, {Lat: 0.1, Lon: 0.015}}
	closed := Closures{Inner: unitWalk, Course: course, Crossings: []geo.LatLon{{Lat: 0.01, Lon: 0.015}}}
	providers := map[string]struct {
		p Provider
		m Mode
	}{
		"subway":        {&Subway{Net: lineNet{}, Walk: unitWalk}, Transit},
		"subway closed": {&Cached{Inner: &Subway{Net: lineNet{}, Walk: closed}}, Transit},
		"haversine":     {Haversine{}, Drive},
		"short detour":  {Haversine{Detour: 0.8}, Bike},
	}
	rng := rand.New(rand.NewSource(1))
	pt := func() geo.LatLon {
		return geo.LatLon{Lat: rng.Float64()*0.04 - 0.02, Lon: rng.Float64()*0.08 - 0.01}
	}
	for name, c := range providers {
		bound := MaxSpeed(c.p, c.m)
		if bound <= 0 {
			t.Fatalf("%s: no bound", name)
		}
		for i := 0; i < 300; i++ {
			a, b := pt(), pt()
			d, ok := c.p.Duration(a, b, c.m)
			if !ok {
				continue
			}
			if geo.Distance(a, b) > bound*d+1e-9 {
				t.Fatalf("%s: %v->%v goes %f m in %f s, above bound %f", name, a, b, geo.Distance(a, b), d, bound)
			}
			steps, ok := Describe(c.p, a, b, c.m)
			if !ok || math.Abs(sumSteps(steps)-d) > 1e-6 {
				t.Fatalf("%s: steps %+v sum %f, duration %f", name, steps, sumSteps(steps), d)
			}
		}
	}
}
