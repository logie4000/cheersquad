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
			prevPos, prevT = s.Node.Pos, s.Node.TFast
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
				rec(nd.Pos, nd.TFast, used|1<<i, stops+1, val+nd.Weight-p.StopPenalty)
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

// pinnedNodes is straightNodes plus a pinned spot at pin meters.
func pinnedNodes(t *testing.T, pin float64) []candidates.Node {
	t.Helper()
	tr, err := gpx.New([]geo.LatLon{{}, {Lat: 10000 / degLat}})
	if err != nil {
		t.Fatal(err)
	}
	tr.Calibrate(10000)
	p := pace.Profile{Distance: 10000, Finish: 3000}
	return candidates.Generate(tr, p, candidates.Options{Spacing: 1000, Pinned: []geo.LatLon{tr.PointAt(pin)}})
}

func TestPinnedSpotIsVisited(t *testing.T) {
	// Unpinned, the cyclist sees every even kilometer. A pin at 3.5 km is
	// off that rhythm, so the plan shifts: 2, 3.5, 5, 7, 9 km (each hop at
	// least 1250 m). Its value counts only real weights, not the pin bonus.
	pl := Solve(Start{}, pinnedNodes(t, 3500), speed(10), Params{Buffer: 250})
	found := false
	for _, s := range pl.Stops {
		if s.Node.Pinned {
			found = true
			if math.Abs(s.Node.Dist-3500) > 10 {
				t.Fatalf("pinned stop at %f, want 3500", s.Node.Dist)
			}
		}
	}
	if !found {
		t.Fatalf("pinned spot not in plan: %v", dists(pl))
	}
	if pl.Value != float64(pl.Sightings) {
		t.Fatalf("value %f includes the pin bonus (sightings %d)", pl.Value, pl.Sightings)
	}
	if got, want := dists(pl), []float64{2000, 3500, 5000, 7000, 9000}; !equal(got, want) {
		t.Fatalf("stops %v, want %v", got, want)
	}
}

func TestUnreachablePinIsSkipped(t *testing.T) {
	// Walking at 1 m/s from the start line, a pin at 5.5 km can never be
	// reached, so the plan is unchanged.
	pl := Solve(Start{}, pinnedNodes(t, 5500), speed(1), Params{})
	if got := dists(pl); !equal(got, []float64{0}) {
		t.Fatalf("stops %v, want [0]", got)
	}
}

func TestPinnedBeatsMoreSightings(t *testing.T) {
	// With one stop allowed, the pin wins over every other spot.
	pl := Solve(Start{}, pinnedNodes(t, 8500), speed(10), Params{Buffer: 250, MaxStops: 1})
	if got := dists(pl); !equal(got, []float64{8500}) {
		t.Fatalf("stops %v, want [8500]", got)
	}
}

func TestLeaveWhenRunnerPasses(t *testing.T) {
	// 10% uncertainty on a 50 min 10 km: spots at 6 km (pass 27-33 min) and
	// 9 km (40.5-49.5 min). A cyclist at 10 m/s takes 300 s for the 3 km
	// hop, plus a 250 s buffer. Leaving when the runner passes 6 km, they
	// have the 3 km at the fastest pace (810 s) to get ahead, so the hop
	// works. Waiting for the slow end at 6 km (1980 s) and still beating the
	// fast end at 9 km (2430 s) would not leave enough time.
	nodes := []candidates.Node{
		{ID: 0, Pos: geo.LatLon{Lat: 6000 / degLat}, Dist: 6000, TFast: 1620, TExpected: 1800, TSlow: 1980, Weight: 1},
		{ID: 1, Pos: geo.LatLon{Lat: 9000 / degLat}, Dist: 9000, TFast: 2430, TExpected: 2700, TSlow: 2970, Weight: 1},
	}
	start := Start{Pos: nodes[0].Pos}
	pl := Solve(start, nodes, speed(10), Params{Buffer: 250})
	if got := dists(pl); !equal(got, []float64{6000, 9000}) {
		t.Fatalf("stops %v, want [6000 9000]", got)
	}
	// Times are for a runner on pace: leave 6 km at 1800, arrive 2100, and
	// the runner is due at 2700.
	s := pl.Stops[1]
	if math.Abs(s.Depart-1800) > 1e-6 || math.Abs(s.Travel-300) > 1e-6 || math.Abs(s.Arrive-2100) > 1e-6 || math.Abs(s.Wait-600) > 1e-6 {
		t.Fatalf("second stop depart %f travel %f arrive %f wait %f", s.Depart, s.Travel, s.Arrive, s.Wait)
	}
	// A bigger hop than the fast stretch allows still fails: with a 600 s
	// buffer, 300 + 600 > 810.
	pl = Solve(start, nodes, speed(10), Params{Buffer: 600})
	if pl.Sightings != 1 {
		t.Fatalf("got %d sightings with a 600 s buffer, want 1", pl.Sightings)
	}
}

func TestLateRaceHopsWithUncertainty(t *testing.T) {
	// A straight 40 km at 0.3 s/m with 5% uncertainty and spots every km.
	// A 10 m/s cyclist gains 0.185 s/m on the runner's fastest pace, so any
	// hop of at least 1352 m clears the 250 s buffer, all the way to the
	// finish. The pass windows grow with distance (+/-9 min at 36 km); if
	// the spectator had to outlast one end and beat the other, hops late
	// in the race would need 7 km or more and the plan would stall.
	tr, err := gpx.New([]geo.LatLon{{}, {Lat: 40000 / degLat}})
	if err != nil {
		t.Fatal(err)
	}
	tr.Calibrate(40000)
	p := pace.Profile{Distance: 40000, Finish: 12000, Uncertainty: 0.05}
	nodes := candidates.Generate(tr, p, candidates.Options{Spacing: 1000})
	pl := Solve(Start{}, nodes, speed(10), Params{Buffer: 250})
	if pl.Sightings < 20 {
		t.Fatalf("only %d sightings: %v", pl.Sightings, dists(pl))
	}
	if last := pl.Stops[len(pl.Stops)-1].Node.Dist; last != 40000 {
		t.Fatalf("plan ends at %.0f m, want the finish: %v", last, dists(pl))
	}
}
