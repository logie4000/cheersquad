package travel

import (
	"github.com/logie4000/cheersquad/internal/geo"
)

// Station is a subway station (a GTFS parent station, or a stop with no
// parent).
type Station struct {
	ID   string     `json:"id"`
	Name string     `json:"name"`
	Pos  geo.LatLon `json:"pos"`
}

// Ride is one subway segment of a trip: board Line at From, get off at To.
type Ride struct {
	Line string `json:"line"`
	// Color is the line's hex color without '#', or "" if the feed has none.
	Color string `json:"color,omitempty"`
	From  int    `json:"-"`
	To    int    `json:"-"`
	// Via lists the stations the train stops at between From and To.
	Via []int `json:"-"`
	// Seconds includes the wait for the train and the ride itself.
	Seconds float64 `json:"seconds"`
}

// Network is a subway system reduced to what routing needs. Only subway and
// metro lines are included: on marathon day buses are rerouted or stuck
// behind closures, so they are not a realistic option.
//
// Station indexes are positions in Stations().
type Network interface {
	// Name is a human label such as "MTA New York City Transit".
	Name() string
	Stations() []Station
	// Time is the fastest seconds from walking into station i to walking
	// out of station j: entry overhead, waits, rides, and transfers. It is
	// false if j cannot be reached from i.
	Time(i, j int) (float64, bool)
	// Rides lists the subway segments of the fastest i->j trip, in order.
	Rides(i, j int) []Ride
	// MaxSpeed is an upper bound in m/s on geo.Distance(station i, station
	// j) / Time(i, j) over all pairs, used for the planner's prefilter.
	MaxSpeed() float64
	// Covers reports whether p is within the network's service area (its
	// stations' bounding box, padded by about 2 km).
	Covers(p geo.LatLon) bool
}

// StepKind is what the spectator does during a step of a leg.
type StepKind string

const (
	StepWalk   StepKind = "walk"
	StepSubway StepKind = "subway"
	StepBike   StepKind = "bike"
	StepDrive  StepKind = "drive"
)

// Step is one part of a travel leg, for showing the spectator how to get
// between stops.
type Step struct {
	Kind StepKind `json:"kind"`
	// Line and Color are set for subway steps.
	Line    string     `json:"line,omitempty"`
	Color   string     `json:"color,omitempty"`
	From    string     `json:"from,omitempty"` // station name, if any
	To      string     `json:"to,omitempty"`
	FromPos geo.LatLon `json:"fromPos"`
	ToPos   geo.LatLon `json:"toPos"`
	// Via lists the stops between From and To on subway steps, so the map
	// can draw the ride along the line instead of as a straight line.
	Via     []Stop  `json:"via,omitempty"`
	Seconds float64 `json:"seconds"`
}

// Stop is an intermediate station on a subway step.
type Stop struct {
	Name string     `json:"name"`
	Pos  geo.LatLon `json:"pos"`
}

// Describer is implemented by providers that can break a leg into steps.
// Steps must sum to the same duration Duration returns for that leg.
type Describer interface {
	Steps(a, b geo.LatLon, m Mode) ([]Step, bool)
}

// SpeedBounder is implemented by providers that can be faster than
// DefaultSpeeds for some mode (a subway beats the transit stub's average).
// MaxSpeed must be an upper bound on geo.Distance(a, b) / Duration(a, b, m)
// for every pair, since the planner skips pairs using it.
type SpeedBounder interface {
	MaxSpeed(m Mode) float64
}
