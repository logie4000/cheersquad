// Package pace models the runner's time at each distance along the course.
// Times are seconds since the gun (race start); distances are meters.
package pace

import "errors"

// Profile describes the runner's expected race.
type Profile struct {
	// Distance is the course length in meters.
	Distance float64 `json:"distance"`
	// Finish is the expected chip time in seconds.
	Finish float64 `json:"finish"`
	// Split is how much slower the second half is than the first, as a
	// fraction: 0.03 is a 3% positive split, -0.02 a 2% negative split.
	// Pace changes linearly over the race to produce it.
	Split float64 `json:"split"`
	// StartDelay is the seconds from the gun until the runner crosses the
	// start mat (wave or corral offset). It turns chip time into gun time.
	StartDelay float64 `json:"startDelay"`
	// Uncertainty widens the pass window: the runner may be this fraction
	// faster or slower than expected (0.05 = +/-5%).
	Uncertainty float64 `json:"uncertainty"`
}

// Validate checks the profile is usable.
func (p Profile) Validate() error {
	switch {
	case p.Distance <= 0:
		return errors.New("pace: distance must be positive")
	case p.Finish <= 0:
		return errors.New("pace: finish time must be positive")
	case p.Split <= -2 || p.Split >= 2:
		return errors.New("pace: split must be between -200% and 200%")
	case p.Uncertainty < 0 || p.Uncertainty >= 1:
		return errors.New("pace: uncertainty must be in [0, 1)")
	case p.StartDelay < 0:
		return errors.New("pace: start delay cannot be negative")
	}
	return nil
}

// chip returns the expected chip time at distance d.
//
// Pace (seconds per meter) is a + b*x where x = d/Distance. With P the
// average pace, integrating gives first half P*D/2 - b*D/8 and second half
// P*D/2 + b*D/8; solving second = (1+Split)*first gives b below.
func (p Profile) chip(d float64) float64 {
	if d < 0 {
		d = 0
	}
	if d > p.Distance {
		d = p.Distance
	}
	avg := p.Finish / p.Distance
	b := 4 * p.Split * avg / (2 + p.Split)
	a := avg - b/2
	x := d / p.Distance
	return p.Distance * (a*x + b*x*x/2)
}

// Expected is the expected gun time at which the runner reaches d.
func (p Profile) Expected(d float64) float64 { return p.StartDelay + p.chip(d) }

// Fast is the earliest plausible gun time at d.
func (p Profile) Fast(d float64) float64 { return p.StartDelay + p.chip(d)*(1-p.Uncertainty) }

// Slow is the latest plausible gun time at d.
func (p Profile) Slow(d float64) float64 { return p.StartDelay + p.chip(d)*(1+p.Uncertainty) }
