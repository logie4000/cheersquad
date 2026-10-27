// Package planner picks the sequence of viewing spots that maximizes the
// total value of runner sightings. It is pure: no network or file I/O. It
// takes nodes, a travel function, and parameters, and returns a plan.
//
// Nodes are sorted by earliest pass time. best[k] is the best total value of
// a plan whose last stop is k:
//
//	best[k] = w[k] + max over feasible j->k of best[j]   (j may be the start)
//	feasible(j, k): t_slow[j] + travel(j, k) + buffer <= t_fast[k]
//
// The spectator leaves a spot only once the runner's latest pass time has
// gone by, and must arrive buffer seconds before the earliest pass time at
// the next one.
package planner

import (
	"math"
	"sort"

	"github.com/logie4000/cheersquad/internal/candidates"
	"github.com/logie4000/cheersquad/internal/geo"
)

// Start is where and when the spectator sets off (seconds since the gun).
type Start struct {
	Pos  geo.LatLon `json:"pos"`
	Time float64    `json:"time"`
}

// TravelFunc returns seconds from a to b, or false if b is unreachable.
type TravelFunc func(a, b geo.LatLon) (float64, bool)

// Params tunes the search.
type Params struct {
	// Buffer is the minimum seconds to arrive before the earliest pass time.
	Buffer float64
	// MaxSpeed is an upper bound on travel speed in m/s. When set, pairs
	// that could not be feasible even in a straight line at this speed are
	// skipped without calling the travel function.
	MaxSpeed float64
	// MaxStops caps the number of stops. Zero means no cap.
	MaxStops int
	// StopPenalty is subtracted from each stop's weight, favoring plans
	// with fewer, better stops.
	StopPenalty float64
}

// Stop is one leg plus the sighting at its end.
type Stop struct {
	Node candidates.Node `json:"node"`
	// Depart is when the spectator leaves the previous stop (or start).
	Depart float64 `json:"depart"`
	// Travel is the leg duration in seconds.
	Travel float64 `json:"travel"`
	// Arrive is Depart + Travel.
	Arrive float64 `json:"arrive"`
	// Wait is the expected wait from arrival to the expected pass time.
	Wait float64 `json:"wait"`
}

// Plan is the planner's answer.
type Plan struct {
	Stops     []Stop  `json:"stops"`
	Sightings int     `json:"sightings"`
	Value     float64 `json:"value"`
}

var negInf = math.Inf(-1)

// Solve returns the highest-value plan. If no spot can be reached in time
// the plan has no stops.
func Solve(start Start, nodes []candidates.Node, travel TravelFunc, p Params) Plan {
	ns := append([]candidates.Node(nil), nodes...)
	sort.SliceStable(ns, func(i, j int) bool { return ns[i].TFast < ns[j].TFast })
	n := len(ns)

	lowerBound := func(a, b geo.LatLon) float64 {
		if p.MaxSpeed <= 0 {
			return 0
		}
		return geo.Distance(a, b) / p.MaxSpeed
	}
	// leg returns the travel time if leaving from at depart reaches node k
	// in time, or NaN if not.
	leg := func(from geo.LatLon, depart float64, k int) float64 {
		if depart+lowerBound(from, ns[k].Pos)+p.Buffer > ns[k].TFast {
			return math.NaN()
		}
		s, ok := travel(from, ns[k].Pos)
		if !ok || depart+s+p.Buffer > ns[k].TFast {
			return math.NaN()
		}
		return s
	}

	fromStart := make([]float64, n)
	legs := make([][]float64, n) // legs[k][j]: travel j->k, j < k
	for k := range ns {
		fromStart[k] = leg(start.Pos, start.Time, k)
		legs[k] = make([]float64, k)
		for j := 0; j < k; j++ {
			legs[k][j] = leg(ns[j].Pos, ns[j].TSlow, k)
		}
	}
	w := make([]float64, n)
	for k := range ns {
		w[k] = ns[k].Weight - p.StopPenalty
	}

	layers := p.MaxStops
	if layers <= 0 || layers > n {
		layers = n
	}
	// With no cap the number of stops does not matter, so one layer that
	// allows any predecessor is enough; with a cap, layer m holds plans of
	// exactly m+1 stops.
	capped := p.MaxStops > 0 && p.MaxStops < n
	if !capped {
		layers = 1
	}
	best := make([][]float64, layers)
	parent := make([][]int, layers)
	for m := range best {
		best[m] = make([]float64, n)
		parent[m] = make([]int, n)
		for k := range ns {
			best[m][k], parent[m][k] = negInf, -1
			if m == 0 && !math.IsNaN(fromStart[k]) {
				best[m][k] = w[k]
			}
			prev := m - 1
			if !capped {
				prev = m
			}
			if prev < 0 {
				continue
			}
			for j := 0; j < k; j++ {
				if math.IsNaN(legs[k][j]) || best[prev][j] == negInf {
					continue
				}
				if v := best[prev][j] + w[k]; v > best[m][k] {
					best[m][k], parent[m][k] = v, j
				}
			}
		}
	}

	bm, bk, bv := -1, -1, 0.0
	for m := range best {
		for k := range ns {
			if best[m][k] != negInf && (bk < 0 || best[m][k] > bv) {
				bm, bk, bv = m, k, best[m][k]
			}
		}
	}
	if bk < 0 {
		return Plan{Stops: []Stop{}}
	}

	var chain []int
	for m, k := bm, bk; k >= 0; {
		chain = append(chain, k)
		k = parent[m][k]
		if capped {
			m--
		}
	}
	plan := Plan{Value: bv, Sightings: len(chain)}
	for i := len(chain) - 1; i >= 0; i-- {
		k := chain[i]
		st := Stop{Node: ns[k], Depart: start.Time, Travel: fromStart[k]}
		if i < len(chain)-1 {
			j := chain[i+1]
			st.Depart, st.Travel = ns[j].TSlow, legs[k][j]
		}
		st.Arrive = st.Depart + st.Travel
		st.Wait = ns[k].TExpected - st.Arrive
		plan.Stops = append(plan.Stops, st)
	}
	return plan
}
