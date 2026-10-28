package travel

import (
	"math"
	"sort"
	"sync"

	"github.com/logie4000/cheersquad/internal/geo"
)

// Defaults for Subway's station search.
const (
	DefaultMaxAccessMeters = 1500
	DefaultNearestStations = 6
)

// Subway routes transit legs over a real subway network: walk to a nearby
// station, ride, walk from a station near the destination. Walking the
// whole way is always an option too, and wins for short hops.
//
// Only Transit uses the network. Any other mode is delegated to Walk with
// that mode unchanged, so a Subway can stand in for its Walk provider.
//
// Walking legs go through Walk, so wrapping Walk in Closures stops them
// crossing the course; the train itself passes under it.
type Subway struct {
	Net Network
	// Walk times the walking parts (always asked with mode Walk).
	Walk Provider
	// MaxAccessMeters is the straight-line distance within which a station
	// is considered for getting on or off. Zero means 1500.
	MaxAccessMeters float64
	// K is how many of the nearest stations within MaxAccessMeters are
	// considered at each end. Zero means 6.
	K int

	mu   sync.Mutex
	near map[geo.LatLon]*access
}

// access is the stations reachable on foot from one point.
type access struct {
	stations []int
	// to[i] is seconds walking from the point to stations[i], from[i] back;
	// NaN when unreachable on foot.
	to, from []float64
}

// nearest returns the K closest stations to p within MaxAccessMeters, by
// straight-line distance, without walking times.
func (s *Subway) nearest(p geo.LatLon) []int {
	maxD := s.MaxAccessMeters
	if maxD <= 0 {
		maxD = DefaultMaxAccessMeters
	}
	k := s.K
	if k <= 0 {
		k = DefaultNearestStations
	}
	type cand struct {
		i int
		d float64
	}
	var cs []cand
	for i, st := range s.Net.Stations() {
		if d := geo.Distance(p, st.Pos); d <= maxD {
			cs = append(cs, cand{i, d})
		}
	}
	sort.Slice(cs, func(a, b int) bool {
		if cs[a].d != cs[b].d {
			return cs[a].d < cs[b].d
		}
		return cs[a].i < cs[b].i
	})
	if len(cs) > k {
		cs = cs[:k]
	}
	out := make([]int, len(cs))
	for i, c := range cs {
		out[i] = c.i
	}
	return out
}

// accessFor returns p's nearby stations with walking times, computed once
// per point: the planner asks about the same few hundred points repeatedly.
func (s *Subway) accessFor(p geo.LatLon) *access {
	s.mu.Lock()
	ac, hit := s.near[p]
	s.mu.Unlock()
	if hit {
		return ac
	}
	stations := s.Net.Stations()
	ac = &access{stations: s.nearest(p)}
	for _, i := range ac.stations {
		ac.to = append(ac.to, walkOrNaN(s.Walk, p, stations[i].Pos))
		ac.from = append(ac.from, walkOrNaN(s.Walk, stations[i].Pos, p))
	}
	s.mu.Lock()
	if s.near == nil {
		s.near = map[geo.LatLon]*access{}
	}
	s.near[p] = ac
	s.mu.Unlock()
	return ac
}

func walkOrNaN(w Provider, a, b geo.LatLon) float64 {
	t, ok := w.Duration(a, b, Walk)
	if !ok {
		return math.NaN()
	}
	return t
}

// StationsNear returns the positions of the stations considered for any of
// ps, without duplicates, so a caller can prefetch walking times to them.
func (s *Subway) StationsNear(ps []geo.LatLon) []geo.LatLon {
	stations := s.Net.Stations()
	seen := map[int]bool{}
	var out []geo.LatLon
	for _, p := range ps {
		for _, i := range s.nearest(p) {
			if !seen[i] {
				seen[i] = true
				out = append(out, stations[i].Pos)
			}
		}
	}
	return out
}

// trip is the fastest way found from a to b. When walkOnly is false the
// spectator rides from station i to station j.
type trip struct {
	seconds  float64
	walkOnly bool
	i, j     int
}

func (s *Subway) best(a, b geo.LatLon) (trip, bool) {
	t := trip{seconds: math.Inf(1)}
	found := false
	if w, ok := s.Walk.Duration(a, b, Walk); ok {
		t, found = trip{seconds: w, walkOnly: true}, true
	}
	from, to := s.accessFor(a), s.accessFor(b)
	for x, i := range from.stations {
		wa := from.to[x]
		if math.IsNaN(wa) || wa >= t.seconds {
			continue
		}
		for y, j := range to.stations {
			wb := to.from[y]
			// Riding from a station back to itself is not a trip.
			if i == j || math.IsNaN(wb) || wa+wb >= t.seconds {
				continue
			}
			r, ok := s.Net.Time(i, j)
			if !ok {
				continue
			}
			if total := wa + r + wb; total < t.seconds {
				t, found = trip{seconds: total, i: i, j: j}, true
			}
		}
	}
	return t, found
}

// Duration is the faster of walking and walk+subway+walk for Transit, and
// Walk's answer for any other mode.
func (s *Subway) Duration(a, b geo.LatLon, m Mode) (float64, bool) {
	if m != Transit {
		return s.Walk.Duration(a, b, m)
	}
	t, ok := s.best(a, b)
	return t.seconds, ok
}

// Steps lists the leg as walk steps (from Walk, so detours through
// crossing points show up), then one subway step per ride, then walk steps.
// Network.Time can exceed the sum of the rides' Seconds (entering the
// station, transfers); that overhead is added to the first subway step, so
// the steps sum to Duration.
func (s *Subway) Steps(a, b geo.LatLon, m Mode) ([]Step, bool) {
	if m != Transit {
		return Describe(s.Walk, a, b, m)
	}
	t, ok := s.best(a, b)
	if !ok {
		return nil, false
	}
	if t.walkOnly {
		return Describe(s.Walk, a, b, Walk)
	}
	stations := s.Net.Stations()
	si, sj := stations[t.i], stations[t.j]
	steps, ok := Describe(s.Walk, a, si.Pos, Walk)
	if !ok {
		return nil, false
	}
	ride, _ := s.Net.Time(t.i, t.j)
	rides := s.Net.Rides(t.i, t.j)
	var subway []Step
	sum := 0.0
	for _, r := range rides {
		from, to := stations[r.From], stations[r.To]
		var via []Stop
		for _, v := range r.Via {
			via = append(via, Stop{Name: stations[v].Name, Pos: stations[v].Pos})
		}
		subway = append(subway, Step{
			Kind: StepSubway, Line: r.Line, Color: r.Color,
			From: from.Name, To: to.Name, FromPos: from.Pos, ToPos: to.Pos,
			Via: via, Path: r.Path, Seconds: r.Seconds,
		})
		sum += r.Seconds
	}
	if len(subway) == 0 {
		subway = []Step{{Kind: StepSubway, From: si.Name, To: sj.Name, FromPos: si.Pos, ToPos: sj.Pos}}
	}
	subway[0].Seconds += ride - sum
	steps = append(steps, subway...)
	tail, ok := Describe(s.Walk, sj.Pos, b, Walk)
	if !ok {
		return nil, false
	}
	return append(steps, tail...), true
}

// MaxSpeed for Transit is the larger of the walking bound and the
// network's: each part of a trip covers at most its own bound times its
// time, and the straight line is no longer than the parts together. It is 0
// (no bound) if Walk has none.
func (s *Subway) MaxSpeed(m Mode) float64 {
	if m != Transit {
		return MaxSpeed(s.Walk, m)
	}
	w := MaxSpeed(s.Walk, Walk)
	if w <= 0 {
		return 0
	}
	return math.Max(w, s.Net.MaxSpeed())
}
