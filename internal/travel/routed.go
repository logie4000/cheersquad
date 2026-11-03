package travel

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/logie4000/cheersquad/internal/geo"
)

// RaceDayAdjust turns a router's normal-day time into a race-day one:
// seconds*Factor + Overhead. Bikes weave through crowds and get locked up;
// cars meet closures and need parking.
type RaceDayAdjust struct {
	Factor   float64
	Overhead float64
}

// DefaultAdjust is the race-day adjustment per mode. Walking slows ORS's
// 5 km/h to the estimate's 1.3 m/s through crowds.
var DefaultAdjust = map[Mode]RaceDayAdjust{
	Walk:  {1.07, 0},
	Bike:  {1.2, 120},
	Drive: {1.5, 600},
}

// Routed is the hybrid of estimates and street routes, for one plan
// request. The planner works with Estimate's times, which are cheap; then
// Steps routes the plan's legs street by street. A routed leg's time
// replaces its estimate from then on, and the other legs' estimates are
// scaled by how routed legs compared with theirs, so the caller can solve
// again with better times (see TakeNew) until a plan's legs are all routed.
//
// Transit is never street routed (its walking parts are, as mode Walk).
// Legs the router cannot answer, or finds no route for, keep the estimate.
type Routed struct {
	Estimate Provider
	Router   StreetRouter
	// AvoidFerries asks the router for routes without ferries.
	AvoidFerries bool
	// Adjust overrides DefaultAdjust per mode.
	Adjust map[Mode]RaceDayAdjust
	// Ctx bounds router requests. Nil means context.Background().
	Ctx context.Context

	mu     sync.Mutex
	frozen bool
	known  map[cacheKey]StreetRoute // adjusted routes by leg
	tried  map[cacheKey]bool        // asked and fell back to the estimate
	fresh  int                      // legs routed since the last TakeNew
	stats  RoutedStats
	// ratios[m] is routed seconds (before the adjustment's overhead) over
	// estimated seconds for every routed leg in mode m; their median scales
	// unrouted estimates.
	ratios map[Mode][]float64
}

// RoutedStats counts what happened to the legs Routed asked about.
type RoutedStats struct {
	Routed      int // got a street route
	NoRoute     int // the router found none
	Unavailable int // the router was down, busy or over budget
}

// Scale limits: one odd leg (a ferry across a river the estimate walks
// over, a bridge far upstream) must not make every estimate wildly off.
const (
	minScale = 0.75
	maxScale = 2.0
)

// Freeze stops Steps routing new legs: from now on they use the estimate,
// so a last solve and its description agree.
func (r *Routed) Freeze() {
	r.mu.Lock()
	r.frozen = true
	r.mu.Unlock()
}

// TakeNew returns how many legs were routed since the last call, and
// resets the count.
func (r *Routed) TakeNew() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.fresh
	r.fresh = 0
	return n
}

// Scale is how much slower (above 1) or faster routed legs in mode m were
// than their estimates (the median, between 0.75 and 2), leaving out the
// race-day overhead; 1 before any are routed.
func (r *Routed) Scale(m Mode) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.scale(m)
}

func (r *Routed) scale(m Mode) float64 {
	rs := r.ratios[m]
	if len(rs) == 0 {
		return 1
	}
	sorted := append([]float64(nil), rs...)
	sort.Float64s(sorted)
	n := len(sorted)
	med := sorted[n/2]
	if n%2 == 0 {
		med = (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return min(maxScale, max(minScale, med))
}

// estimated turns an estimate into what the street route would likely
// say: scaled, plus the race-day overhead once any leg in mode m has been
// routed (before that the estimate stands, as it already allows for
// parking and the like). The caller holds r.mu.
func (r *Routed) estimated(s float64, m Mode) float64 {
	if len(r.ratios[m]) == 0 {
		return s
	}
	return s*r.scale(m) + r.adjust(m).Overhead
}

// Stats reports what happened to the legs asked about so far.
func (r *Routed) Stats() RoutedStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

func (r *Routed) adjust(m Mode) RaceDayAdjust {
	if a, ok := r.Adjust[m]; ok {
		return a
	}
	if a, ok := DefaultAdjust[m]; ok {
		return a
	}
	return RaceDayAdjust{1, 0}
}

// Duration is the routed time for legs already routed, else the scaled
// estimate.
func (r *Routed) Duration(a, b geo.LatLon, m Mode) (float64, bool) {
	k := cacheKey{a, b, m}
	r.mu.Lock()
	sr, hit := r.known[k]
	r.mu.Unlock()
	if hit {
		return sr.Seconds, true
	}
	s, ok := r.Estimate.Duration(a, b, m)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.estimated(s, m), ok
}

// Steps routes the leg if it has not been tried (and Routed is not
// frozen), and returns it as one step along the street path, or the
// estimate's steps if there is no route.
func (r *Routed) Steps(a, b geo.LatLon, m Mode) ([]Step, bool) {
	k := cacheKey{a, b, m}
	if _, routable := orsProfiles[m]; !routable || a == b {
		return r.estimateSteps(a, b, m)
	}
	r.mu.Lock()
	sr, hit := r.known[k]
	skip := r.frozen || r.tried[k]
	r.mu.Unlock()
	if !hit && !skip {
		sr, hit = r.route(k)
	}
	if !hit {
		return r.estimateSteps(a, b, m)
	}
	return []Step{{Kind: StepKindFor(m), FromPos: a, ToPos: b, Path: sr.Path, Seconds: sr.Seconds}}, true
}

// estimateSteps is the estimate's steps, adjusted to match Duration (the
// overhead goes on the first step).
func (r *Routed) estimateSteps(a, b geo.LatLon, m Mode) ([]Step, bool) {
	steps, ok := Describe(r.Estimate, a, b, m)
	if !ok || len(steps) == 0 {
		return steps, ok
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.ratios[m]) == 0 {
		return steps, ok
	}
	sc := r.scale(m)
	for i := range steps {
		steps[i].Seconds *= sc
	}
	steps[0].Seconds += r.adjust(m).Overhead
	return steps, ok
}

func (r *Routed) route(k cacheKey) (StreetRoute, bool) {
	ctx := r.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return StreetRoute{}, false
	}
	sr, err := r.Router.Route(ctx, k.a, k.b, k.m, r.AvoidFerries)
	est, estOK := r.Estimate.Duration(k.a, k.b, k.m)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		if r.tried == nil {
			r.tried = map[cacheKey]bool{}
		}
		r.tried[k] = true
		if errors.Is(err, ErrNoStreetRoute) {
			r.stats.NoRoute++
		} else {
			r.stats.Unavailable++
		}
		return StreetRoute{}, false
	}
	adj := r.adjust(k.m)
	raw := sr.Seconds * adj.Factor
	sr.Seconds = raw + adj.Overhead
	if r.known == nil {
		r.known = map[cacheKey]StreetRoute{}
	}
	r.known[k] = sr
	r.fresh++
	r.stats.Routed++
	if estOK && est > 0 {
		if r.ratios == nil {
			r.ratios = map[Mode][]float64{}
		}
		r.ratios[k.m] = append(r.ratios[k.m], raw/est)
	}
	return sr, true
}

// MaxSpeed is the estimate's bound, raised for a scale below 1 and to
// cover every routed leg (a street route can beat the estimate). It is 0
// (no bound) if the estimate has none. Legs are only routed between
// solves, so the bound holds for a whole solve.
func (r *Routed) MaxSpeed(m Mode) float64 {
	b := MaxSpeed(r.Estimate, m)
	if b <= 0 {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.scale(m); s < 1 {
		b /= s
	}
	for k, sr := range r.known {
		if k.m != m {
			continue
		}
		if sr.Seconds <= 0 {
			return 0
		}
		b = max(b, geo.Distance(k.a, k.b)/sr.Seconds)
	}
	return b
}
