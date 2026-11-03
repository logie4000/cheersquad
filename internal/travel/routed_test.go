package travel

import (
	"context"
	"math"
	"testing"

	"github.com/logie4000/cheersquad/internal/geo"
)

// slowRouter routes along the straight line at Factor times the
// straight-line distance in seconds, or fails with Err.
type slowRouter struct {
	Factor float64
	Err    error
	calls  int
}

func (s *slowRouter) Route(_ context.Context, a, b geo.LatLon, m Mode, _ bool) (StreetRoute, error) {
	s.calls++
	if s.Err != nil {
		return StreetRoute{}, s.Err
	}
	d := geo.Distance(a, b)
	return StreetRoute{Seconds: d * s.Factor, Meters: d, Path: []geo.LatLon{a, b}}, nil
}

func TestRoutedPinsRoutedTimes(t *testing.T) {
	sr := &slowRouter{Factor: 2} // 0.5 m/s, slower than the 1.3 m/s estimate
	r := &Routed{Estimate: Haversine{}, Router: sr}
	a, b := geo.LatLon{}, geo.LatLon{Lat: 0.01}
	d := geo.Distance(a, b)
	est, _ := Haversine{}.Duration(a, b, Walk)
	if s, _ := r.Duration(a, b, Walk); s != est {
		t.Fatalf("before routing: %f, want the estimate %f", s, est)
	}
	steps, ok := r.Steps(a, b, Walk)
	if !ok || len(steps) != 1 || math.Abs(steps[0].Seconds-2*d*1.07) > 1e-9 || len(steps[0].Path) != 2 || steps[0].Kind != StepWalk {
		t.Fatalf("steps %+v", steps)
	}
	if s, _ := r.Duration(a, b, Walk); math.Abs(s-2*d*1.07) > 1e-9 {
		t.Fatalf("after routing: %f, want %f", s, 2*d*1.07)
	}
	if n := r.TakeNew(); n != 1 {
		t.Fatalf("TakeNew %d", n)
	}
	if n := r.TakeNew(); n != 0 {
		t.Fatal("TakeNew should reset")
	}
	r.Steps(a, b, Walk)
	if sr.calls != 1 {
		t.Fatalf("%d router calls, want 1", sr.calls)
	}
	// Other legs' estimates scale by how the routed one compared.
	c := geo.LatLon{Lat: 0.02}
	estC, _ := Haversine{}.Duration(a, c, Walk)
	scale := min(2, 2*d*1.07/est) // routed over estimated seconds, capped at 2
	if s, _ := r.Duration(a, c, Walk); math.Abs(s-estC*scale) > 1e-6 {
		t.Fatalf("unrouted leg %f, want %f scaled by %f", s, estC, scale)
	}
	// Frozen: new legs keep the scaled estimate and are not routed.
	r.Freeze()
	steps, _ = r.Steps(a, c, Walk)
	if sr.calls != 1 || len(steps[0].Path) != 0 || math.Abs(steps[0].Seconds-estC*scale) > 1e-6 {
		t.Fatalf("frozen leg: %+v", steps)
	}
}

func TestRoutedAdjustsAndBounds(t *testing.T) {
	r := &Routed{Estimate: Haversine{}, Router: &slowRouter{Factor: 0.05}} // 20 m/s by car
	a, b := geo.LatLon{}, geo.LatLon{Lat: 0.1}
	d := geo.Distance(a, b)
	if got := r.MaxSpeed(Drive); got != (Haversine{}).MaxSpeed(Drive) {
		t.Fatalf("bound before routing %f", got)
	}
	steps, _ := r.Steps(a, b, Drive)
	want := d*0.05*1.5 + 600
	if math.Abs(steps[0].Seconds-want) > 1e-9 || steps[0].Kind != StepDrive {
		t.Fatalf("drive %+v, want %f s", steps, want)
	}
	if s := r.Scale(Drive); s >= 1 {
		t.Fatalf("a faster route should scale drive estimates down, got %f", s)
	}
	if got := r.MaxSpeed(Drive); got < d/want-1e-9 {
		t.Fatalf("bound %f must cover the routed leg's %f m/s", got, d/want)
	}
	if got := r.MaxSpeed(Walk); got != (Haversine{}).MaxSpeed(Walk) {
		t.Fatalf("walk bound changed by a drive route: %f", got)
	}
}

func TestRoutedFallsBack(t *testing.T) {
	a, b := geo.LatLon{}, geo.LatLon{Lat: 0.01}
	est, _ := Haversine{}.Duration(a, b, Walk)
	for _, err := range []error{ErrRouterUnavailable, ErrNoStreetRoute} {
		sr := &slowRouter{Err: err}
		r := &Routed{Estimate: Haversine{}, Router: sr}
		steps, ok := r.Steps(a, b, Walk)
		if !ok || steps[0].Seconds != est || len(steps[0].Path) != 0 {
			t.Fatalf("%v: %+v", err, steps)
		}
		r.Steps(a, b, Walk)
		want := RoutedStats{NoRoute: 1}
		if err == ErrRouterUnavailable {
			want = RoutedStats{Unavailable: 1}
		}
		if got := r.Stats(); got != want || sr.calls != 1 {
			t.Fatalf("%v: stats %+v after %d calls", err, got, sr.calls)
		}
	}
	// Transit is never street routed.
	sr := &slowRouter{Factor: 1}
	r := &Routed{Estimate: Haversine{}, Router: sr}
	r.Steps(a, b, Transit)
	if sr.calls != 0 {
		t.Fatal("transit leg was street routed")
	}
}

// One leg far faster than its estimate (a ferry over a river the estimate
// walks across) must not speed up every other estimate.
func TestRoutedScaleIgnoresOddLeg(t *testing.T) {
	r := &Routed{Estimate: Haversine{}}
	r.ratios = map[Mode][]float64{Walk: {1.1, 1.2, 0.2}}
	if s := r.Scale(Walk); s != 1.1 {
		t.Fatalf("scale %f, want the median 1.1", s)
	}
	r.ratios[Walk], r.scales = []float64{0.1}, nil
	if s := r.Scale(Walk); s != minScale {
		t.Fatalf("scale %f, want the floor %f", s, minScale)
	}
}
