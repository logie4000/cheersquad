package travel

import (
	"math"

	"github.com/logie4000/cheersquad/internal/geo"
)

// StepTransit is a leg estimated by the straight-line transit stub, when no
// subway data covers the course. It says "some public transit", not a
// particular train.
const StepTransit StepKind = "transit"

// Prefetcher is implemented by providers that can answer many pairs at once
// (a routing API's table endpoint). Prefetch warms the provider's cache for
// every ordered pair of points so later Duration calls make no requests.
type Prefetcher interface {
	Prefetch(points []geo.LatLon)
}

// StepKindFor returns the step kind a single-step leg in mode m has.
func StepKindFor(m Mode) StepKind {
	switch m {
	case Bike:
		return StepBike
	case Drive:
		return StepDrive
	case Transit:
		return StepTransit
	}
	return StepWalk
}

// Describe breaks the leg a->b into steps using p's Describer if it has one,
// and otherwise as a single step of the mode's kind lasting p.Duration.
func Describe(p Provider, a, b geo.LatLon, m Mode) ([]Step, bool) {
	if d, ok := p.(Describer); ok {
		return d.Steps(a, b, m)
	}
	s, ok := p.Duration(a, b, m)
	if !ok {
		return nil, false
	}
	return []Step{{Kind: StepKindFor(m), FromPos: a, ToPos: b, Seconds: s}}, true
}

// MaxSpeed returns p's speed bound for mode m if p is a SpeedBounder, or 0
// (no bound known, so callers must not prefilter) otherwise.
func MaxSpeed(p Provider, m Mode) float64 {
	if sb, ok := p.(SpeedBounder); ok {
		return sb.MaxSpeed(m)
	}
	return 0
}

func (h Haversine) detour() float64 {
	if h.Detour == 0 {
		return 1.3
	}
	return h.Detour
}

// Steps is a single straight-line step of the mode's kind.
func (h Haversine) Steps(a, b geo.LatLon, m Mode) ([]Step, bool) {
	s, ok := h.Duration(a, b, m)
	if !ok {
		return nil, false
	}
	return []Step{{Kind: StepKindFor(m), FromPos: a, ToPos: b, Seconds: s}}, true
}

// MaxSpeed is the mode's speed, divided by the detour factor when that is
// below 1 (a detour under 1 makes legs faster than the straight line).
func (h Haversine) MaxSpeed(m Mode) float64 {
	return h.Speed(m) / math.Min(1, h.detour())
}

func (h Haversine) String() string { return "straight-line estimate" }

// Steps passes through to Inner (uncached: it is called once per plan leg).
func (c *Cached) Steps(a, b geo.LatLon, m Mode) ([]Step, bool) {
	return Describe(c.Inner, a, b, m)
}

// MaxSpeed passes through to Inner, or 0 (no bound) if Inner has none.
func (c *Cached) MaxSpeed(m Mode) float64 { return MaxSpeed(c.Inner, m) }

// Steps describes the leg with Inner, split at the crossing point when the
// leg has to detour through one.
func (c Closures) Steps(a, b geo.LatLon, m Mode) ([]Step, bool) {
	x, via, _, ok := c.route(a, b, m)
	if !ok {
		return nil, false
	}
	if !via {
		return Describe(c.Inner, a, b, m)
	}
	s1, ok1 := Describe(c.Inner, a, x, m)
	s2, ok2 := Describe(c.Inner, x, b, m)
	if !ok1 || !ok2 {
		return nil, false
	}
	return append(s1, s2...), true
}

// MaxSpeed passes through to Inner: a detour through a crossing point is
// never shorter than the straight line, so Inner's bound still holds.
func (c Closures) MaxSpeed(m Mode) float64 { return MaxSpeed(c.Inner, m) }
