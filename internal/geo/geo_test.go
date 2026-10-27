package geo

import (
	"math"
	"testing"
)

func TestDistanceOneDegreeLatitude(t *testing.T) {
	d := Distance(LatLon{0, 0}, LatLon{1, 0})
	want := 2 * math.Pi * EarthRadius / 360
	if math.Abs(d-want) > 0.01 {
		t.Fatalf("got %f want %f", d, want)
	}
}

func TestSegmentIntersection(t *testing.T) {
	// A plus sign: horizontal and vertical segments crossing in the middle.
	tt, ok := SegmentIntersection(LatLon{0, -0.01}, LatLon{0, 0.01}, LatLon{-0.01, 0}, LatLon{0.01, 0})
	if !ok || math.Abs(tt-0.5) > 1e-6 {
		t.Fatalf("got t=%f ok=%v", tt, ok)
	}
	// Parallel segments never cross.
	if _, ok := SegmentIntersection(LatLon{0, 0}, LatLon{0, 1}, LatLon{1, 0}, LatLon{1, 1}); ok {
		t.Fatal("parallel segments reported as crossing")
	}
	// Segments that would cross only if extended.
	if _, ok := SegmentIntersection(LatLon{0, -0.01}, LatLon{0, -0.005}, LatLon{-0.01, 0}, LatLon{0.01, 0}); ok {
		t.Fatal("non-overlapping segments reported as crossing")
	}
}

func TestProjectOnSegment(t *testing.T) {
	f, d := ProjectOnSegment(LatLon{0.001, 0.005}, LatLon{0, 0}, LatLon{0, 0.01})
	if math.Abs(f-0.5) > 1e-6 {
		t.Fatalf("f=%f", f)
	}
	if math.Abs(d-Distance(LatLon{0, 0}, LatLon{0.001, 0})) > 0.5 {
		t.Fatalf("d=%f", d)
	}
}
