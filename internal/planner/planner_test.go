package planner

import (
	"math"
	"math/rand"
	"testing"

	"github.com/logie4000/cheersquad/internal/candidates"
	"github.com/logie4000/cheersquad/internal/geo"
	"github.com/logie4000/cheersquad/internal/gpx"
	"github.com/logie4000/cheersquad/internal/pace"
	"github.com/logie4000/cheersquad/internal/travel"
)

var degLat = geo.Distance(geo.LatLon{}, geo.LatLon{Lat: 1})

// speed returns a straight-line travel function at v m/s.
func speed(v float64) TravelFunc {
	h := travel.Haversine{Speeds: map[travel.Mode]float64{travel.Walk: v}, Detour: 1}
	return func(a, b geo.LatLon) (float64, bool) { return h.Duration(a, b, travel.Walk) }
}

// straightNodes is a 10 km course due north, runner at 0.3 s/m (50 min
// finish), no uncertainty, spots every 1 km.
func straightNodes(t *testing.T) []candidates.Node {
	t.Helper()
	tr, err := gpx.New([]geo.LatLon{{}, {Lat: 10000 / degLat}})
	if err != nil {
		t.Fatal(err)
	}
	tr.Calibrate(10000)
	p := pace.Profile{Distance: 10000, Finish: 3000}
	return candidates.Generate(tr, p, candidates.Options{Spacing: 1000})
}

func dists(pl Plan) []float64 {
	var out []float64
	for _, s := range pl.Stops {
		out = append(out, math.Round(s.Node.Dist))
	}
	return out
}

func TestStraightCourseLeapfrog(t *testing.T) {
	// A cyclist at 10 m/s gains 0.2 s per meter on the runner. With a 250 s
	// buffer each hop must be at least 1250 m, so the best plan is every
	// other kilometer, starting at 2 km.
	pl := Solve(Start{}, straightNodes(t), speed(10), Params{Buffer: 250})
	want := []float64{2000, 4000, 6000, 8000, 10000}
	if got := dists(pl); !equal(got, want) {
		t.Fatalf("stops %v, want %v", got, want)
	}
	if pl.Sightings != 5 || pl.Value != 5 {
		t.Fatalf("sightings %d value %f", pl.Sightings, pl.Value)
	}
	for _, s := range pl.Stops {
		if s.Wait < 250-1e-6 {
			t.Fatalf("wait %f below buffer at %f", s.Wait, s.Node.Dist)
		}
	}
}

func TestMaxStopsAndPenalty(t *testing.T) {
	nodes := straightNodes(t)
	pl := Solve(Start{}, nodes, speed(10), Params{Buffer: 250, MaxStops: 2})
	if pl.Sightings != 2 {
		t.Fatalf("got %d stops with cap 2", pl.Sightings)
	}
	pl = Solve(Start{}, nodes, speed(10), Params{Buffer: 250, StopPenalty: 2})
	if pl.Sightings != 1 || pl.Value != -1 {
		t.Fatalf("with penalty larger than weight: %d stops, value %f", pl.Sightings, pl.Value)
	}
}

func TestTooSlowForSecondSpot(t *testing.T) {
	// Walking at 1 m/s from the start line, the spectator sees the start and
	// can never catch the runner again.
	pl := Solve(Start{}, straightNodes(t), speed(1), Params{})
	if got := dists(pl); !equal(got, []float64{0}) {
		t.Fatalf("stops %v, want [0]", got)
	}
}

func TestNothingReachable(t *testing.T) {
	far := Start{Pos: geo.LatLon{Lat: 10}, Time: 0}
	pl := Solve(far, straightNodes(t), speed(1), Params{})
	if len(pl.Stops) != 0 || pl.Sightings != 0 {
		t.Fatalf("expected empty plan, got %+v", pl)
	}
}

func TestOutAndBackSeesRunnerTwice(t *testing.T) {
	// 5 km out, 5 km back. A slow walker who waits near the turnaround sees
	// the runner on the way out and again on the way back.
	tr, err := gpx.New([]geo.LatLon{{}, {Lat: 5000 / degLat}, {Lon: 0.00001}})
	if err != nil {
		t.Fatal(err)
	}
	p := pace.Profile{Distance: tr.Length(), Finish: 3000, Uncertainty: 0.02}
	// Spots every 500 m put nodes at 4.5 km out and 5.5 km (the same place)
	// on the way back.
	nodes := candidates.Generate(tr, p, candidates.Options{Spacing: 500})
	start := Start{Pos: tr.PointAt(4500), Time: 0}
	pl := Solve(start, nodes, speed(0.5), Params{Buffer: 60})
	if pl.Sightings < 2 {
		t.Fatalf("got %d sightings", pl.Sightings)
	}
	out, back := false, false
	for _, s := range pl.Stops {
		if s.Node.Dist < 5000 {
			out = true
		} else {
			back = true
		}
	}
	if !out || !back {
		t.Fatalf("expected stops on both legs: %v", dists(pl))
	}
}

// TestMatchesBruteForce checks the DP against exhaustive search on small
// random instances, and that every returned plan is feasible.
func TestMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 300; iter++ {
		n := 2 + rng.Intn(8)
		nodes := make([]candidates.Node, n)
		for i := range nodes {
			tf := rng.Float64() * 3000
			nodes[i] = candidates.Node{
				ID:        i,
				Pos:       geo.LatLon{Lat: rng.Float64() * 0.02, Lon: rng.Float64() * 0.02},
				TFast:     tf,
				TExpected: tf + 30,
				TSlow:     tf + rng.Float64()*120,
				Weight:    0.5 + rng.Float64()*2,
			}
		}
		start := Start{Pos: geo.LatLon{Lat: 0.01, Lon: 0.01}, Time: -600}
		params := Params{Buffer: rng.Float64() * 120, MaxSpeed: 3, MaxStops: rng.Intn(4)}
		tf := speed(3)

		pl := Solve(start, nodes, tf, params)
		want := bruteForce(start, nodes, tf, params)
		if math.Abs(pl.Value-want) > 1e-9 {
			t.Fatalf("iter %d: DP value %f, brute force %f", iter, pl.Value, want)
		}
		if params.MaxStops > 0 && pl.Sightings > params.MaxStops {
			t.Fatalf("iter %d: %d stops over cap %d", iter, pl.Sightings, params.MaxStops)
		}
		prevPos, prevT := start.Pos, start.Time
		for _, s := range pl.Stops {
			d, ok := tf(prevPos, s.Node.Pos)
			if !ok || prevT+d+params.Buffer > s.Node.TFast+1e-9 {
				t.Fatalf("iter %d: infeasible leg into node %d", iter, s.Node.ID)
			}
			prevPos, prevT = s.Node.Pos, s.Node.TSlow
		}
	}
}

func bruteForce(start Start, nodes []candidates.Node, tf TravelFunc, p Params) float64 {
	best, found := 0.0, false
	var rec func(pos geo.LatLon, t float64, used uint, stops int, val float64)
	rec = func(pos geo.LatLon, t float64, used uint, stops int, val float64) {
		if stops > 0 && (!found || val > best) {
			best, found = val, true
		}
		if p.MaxStops > 0 && stops == p.MaxStops {
			return
		}
		for i, nd := range nodes {
			if used&(1<<i) != 0 {
				continue
			}
			d, ok := tf(pos, nd.Pos)
			if ok && t+d+p.Buffer <= nd.TFast {
				rec(nd.Pos, nd.TSlow, used|1<<i, stops+1, val+nd.Weight-p.StopPenalty)
			}
		}
	}
	rec(start.Pos, start.Time, 0, 0, 0)
	return best
}

func equal(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
