// Package candidates generates the viewing spots the planner chooses from.
//
// A node is a place on the course at a particular course distance, so on an
// out-and-back or loop the same physical spot yields one node per pass.
package candidates

import (
	"math"
	"sort"

	"github.com/logie4000/cheersquad/internal/geo"
	"github.com/logie4000/cheersquad/internal/gpx"
	"github.com/logie4000/cheersquad/internal/pace"
)

// MileMeters is one statute mile in meters.
const MileMeters = 1609.344

// Node is one candidate viewing opportunity.
type Node struct {
	ID  int        `json:"id"`
	Pos geo.LatLon `json:"pos"`
	// Dist is the course distance in meters.
	Dist float64 `json:"dist"`
	// TFast, TExpected and TSlow are the earliest, expected and latest
	// runner pass times in seconds since the gun.
	TFast     float64 `json:"tFast"`
	TExpected float64 `json:"tExpected"`
	TSlow     float64 `json:"tSlow"`
	Weight    float64 `json:"weight"`
	Pinned    bool    `json:"pinned"`
}

// Mile is the 1-based mile the node falls in (mile 1 covers 0 to 1609 m).
func (n Node) Mile() int { return int(n.Dist/MileMeters) + 1 }

// Area is a circular region, used to exclude spots.
type Area struct {
	Center geo.LatLon `json:"center"`
	Radius float64    `json:"radius"`
}

// DefaultSnapRadius is how far, in meters, a pinned location may be from the
// course by default.
const DefaultSnapRadius = 100.0

// Options controls candidate generation. Zero values get defaults.
type Options struct {
	// Spacing between generated spots in meters. Default 300.
	Spacing float64
	// Pinned locations are snapped to the course and added, once per pass.
	Pinned []geo.LatLon
	// SnapRadius is how far a pinned location may be from the course.
	// Default DefaultSnapRadius.
	SnapRadius float64
	// Excluded areas drop any spot inside them, pinned or generated.
	Excluded []Area
	// MileWeights overrides the default weight of 1 for spots in a mile.
	MileWeights map[int]float64
}

// Generate returns candidate nodes sorted by course distance.
func Generate(t *gpx.Track, p pace.Profile, o Options) []Node {
	if o.Spacing <= 0 {
		o.Spacing = 300
	}
	if o.SnapRadius <= 0 {
		o.SnapRadius = DefaultSnapRadius
	}
	var dists []float64
	for d := 0.0; d < t.Length(); d += o.Spacing {
		dists = append(dists, d)
	}
	dists = append(dists, t.Length())

	pinned := map[float64]bool{}
	for _, pin := range o.Pinned {
		for _, d := range snap(t, pin, o.SnapRadius) {
			dists = append(dists, d)
			pinned[d] = true
		}
	}
	sort.Float64s(dists)

	var nodes []Node
	for _, d := range dists {
		pos := t.PointAt(d)
		if excluded(pos, o.Excluded) {
			continue
		}
		n := Node{
			ID:        len(nodes),
			Pos:       pos,
			Dist:      d,
			TFast:     p.Fast(d),
			TExpected: p.Expected(d),
			TSlow:     p.Slow(d),
			Weight:    1,
			Pinned:    pinned[d],
		}
		if w, ok := o.MileWeights[n.Mile()]; ok {
			n.Weight = w
		}
		nodes = append(nodes, n)
	}
	return nodes
}

func excluded(p geo.LatLon, areas []Area) bool {
	for _, a := range areas {
		if geo.Distance(p, a.Center) <= a.Radius {
			return true
		}
	}
	return false
}

// snap returns the course distance of the closest point on each pass of the
// course within radius of p. The course is sampled every few meters; a pass
// is a run of consecutive samples within radius, so a long segment that
// goes out and comes back on itself still counts as two passes.
func snap(t *gpx.Track, p geo.LatLon, radius float64) []float64 {
	step := math.Min(10, radius/4)
	var out []float64
	best, bestD := math.Inf(1), 0.0
	for d := 0.0; ; d += step {
		if d > t.Length() {
			d = t.Length()
		}
		dist := geo.Distance(p, t.PointAt(d))
		if dist <= radius && dist < best {
			best, bestD = dist, d
		}
		if dist > radius && !math.IsInf(best, 1) {
			out = append(out, bestD)
			best = math.Inf(1)
		}
		if d == t.Length() {
			break
		}
	}
	if !math.IsInf(best, 1) {
		out = append(out, bestD)
	}
	return out
}
