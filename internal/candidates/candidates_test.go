package candidates

import (
	"math"
	"testing"

	"github.com/logie4000/cheersquad/internal/geo"
	"github.com/logie4000/cheersquad/internal/gpx"
	"github.com/logie4000/cheersquad/internal/pace"
)

// straight is a course heading due north, calibrated to exactly length meters.
func straight(t *testing.T, length float64) *gpx.Track {
	t.Helper()
	deg := length / geo.Distance(geo.LatLon{}, geo.LatLon{Lat: 1})
	tr, err := gpx.New([]geo.LatLon{{}, {Lat: deg / 2}, {Lat: deg}})
	if err != nil {
		t.Fatal(err)
	}
	tr.Calibrate(length)
	return tr
}

// outAndBack runs north for half meters and returns along the same line.
func outAndBack(t *testing.T, half float64) *gpx.Track {
	t.Helper()
	deg := half / geo.Distance(geo.LatLon{}, geo.LatLon{Lat: 1})
	tr, err := gpx.New([]geo.LatLon{{}, {Lat: deg}, {Lat: 0, Lon: 0.00001}})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestSpacingAndTimes(t *testing.T) {
	tr := straight(t, 1000)
	p := pace.Profile{Distance: 1000, Finish: 300, Uncertainty: 0.1}
	nodes := Generate(tr, p, Options{Spacing: 300})
	wantD := []float64{0, 300, 600, 900, 1000}
	if len(nodes) != len(wantD) {
		t.Fatalf("got %d nodes, want %d", len(nodes), len(wantD))
	}
	for i, n := range nodes {
		if math.Abs(n.Dist-wantD[i]) > 1e-9 || n.ID != i {
			t.Fatalf("node %d: %+v", i, n)
		}
		if math.Abs(n.TExpected-0.3*n.Dist) > 1e-9 || n.TFast > n.TExpected || n.TSlow < n.TExpected {
			t.Fatalf("node %d times: %+v", i, n)
		}
	}
}

func TestExcludedAndMileWeights(t *testing.T) {
	tr := straight(t, 4000)
	p := pace.Profile{Distance: 4000, Finish: 1200}
	mid := tr.PointAt(2000)
	nodes := Generate(tr, p, Options{
		Spacing:     500,
		Excluded:    []Area{{Center: mid, Radius: 10}},
		MileWeights: map[int]float64{2: 3},
	})
	for _, n := range nodes {
		if n.Dist == 2000 {
			t.Fatal("excluded node kept")
		}
		want := 1.0
		if n.Dist >= MileMeters && n.Dist < 2*MileMeters {
			want = 3
		}
		if n.Weight != want {
			t.Fatalf("node at %f weight %f want %f", n.Dist, n.Weight, want)
		}
	}
}

func TestPinnedSnapsOncePerPass(t *testing.T) {
	tr := outAndBack(t, 2000)
	p := pace.Profile{Distance: tr.Length(), Finish: 1200}
	// A spot 30 m east of the course, 500 m from the start: passed twice.
	pin := geo.LatLon{Lat: tr.PointAt(500).Lat, Lon: 0.0003}
	nodes := Generate(tr, p, Options{Spacing: 1e6, Pinned: []geo.LatLon{pin}})
	var got []Node
	for _, n := range nodes {
		if n.Pinned {
			got = append(got, n)
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %d pinned nodes, want 2: %+v", len(got), got)
	}
	if math.Abs(got[0].Dist-500) > 5 || math.Abs(got[1].Dist-(tr.Length()-500)) > 5 {
		t.Fatalf("pinned distances %f, %f", got[0].Dist, got[1].Dist)
	}
	if got[1].TFast <= got[0].TFast {
		t.Fatal("second pass should be later")
	}
}
