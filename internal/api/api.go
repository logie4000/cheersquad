// Package api exposes the planner over HTTP. It is the edge where request
// units (clock times, JSON shapes) are converted to the core's seconds since
// the gun and meters.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/logie4000/cheersquad/internal/candidates"
	"github.com/logie4000/cheersquad/internal/geo"
	"github.com/logie4000/cheersquad/internal/gpx"
	"github.com/logie4000/cheersquad/internal/pace"
	"github.com/logie4000/cheersquad/internal/planner"
	"github.com/logie4000/cheersquad/internal/races"
	"github.com/logie4000/cheersquad/internal/travel"
)

// MaxUpload caps the request body (GPX plus params). Course GPX files are
// typically under 250 KB; a per-second watch recording of a marathon is
// a few MB. Keep in step with client_max_body_size in the Nginx snippet.
const MaxUpload = 3 << 20

// Limits on a single plan request. The planner keeps a travel time for
// every pair of candidates, so memory and time grow with the square of the
// candidate count; these keep one request from exhausting the container.
const (
	// MaxCourseMeters allows ultras up to 100 km.
	MaxCourseMeters = 100_000
	// MaxPoints is far more than any real course needs (a marathon recorded
	// every second is about 15k points).
	MaxPoints = 200_000
	// MinSpacingMeters is the closest candidate spots may be.
	MinSpacingMeters = 50
	// MaxCandidates bounds the planner's pairwise table.
	MaxCandidates = 2500
	// MaxStopsLimit caps an explicit stop limit (0 still means no limit).
	MaxStopsLimit = 50
	// MaxMarks caps pinned spots, excluded areas and crossings each.
	MaxMarks = 50
	// MaxConcurrentPlans is how many plans may run at once. A typical plan
	// takes a few to a few tens of milliseconds, so more wait their turn.
	MaxConcurrentPlans = 4
	// MaxCrossingPlans is how many of the running plans may have crossing
	// points. Closure checks make those up to a few hundred times slower,
	// so this keeps slots free for everyone else.
	MaxCrossingPlans = 2
	// MaxQueuedPlans is how many requests may wait for a slot; beyond that
	// they get a 503 straight away, which bounds the memory held by request
	// bodies waiting in line.
	MaxQueuedPlans = 32
	// QueueWait is how long a request waits for a slot before a 503.
	QueueWait = 10 * time.Second
	// MaxFinishSeconds is 48 hours; slower than that is not a real pace and
	// would make every pair of spots reachable.
	MaxFinishSeconds = 48 * 3600
	// MaxMileWeights caps the per-mile weight map.
	MaxMileWeights = 200
	// MaxPrefetchPoints is the most points whose pairwise walking times are
	// fetched up front; beyond it they are fetched on demand, after the
	// planner's straight-line prefilter.
	MaxPrefetchPoints = 1500
	// MaxClosurePoints is how finely the course is kept for checking whether
	// a leg crosses it (every check walks all of it).
	MaxClosurePoints = 2000
	// PlanTimeout stops a plan that runs too long. QueueWait plus this stays
	// within the server's 60s write timeout.
	PlanTimeout = 30 * time.Second
	// CrossingPlanTimeout is the shorter limit for plans with crossing
	// points, so a slow one gives its slot back sooner.
	CrossingPlanTimeout = 15 * time.Second
	// MaxRouteRounds is how many times a plan's legs are street routed and
	// the plan solved again with the routed times. Each round costs a
	// router request for each leg not yet routed (more for transit).
	MaxRouteRounds = 3
	// MaxRepairRounds is how many times, after those, the plan is solved
	// again over just its own spots, so it ends up on street-routed legs
	// rather than chasing new spots whose legs are still estimates.
	MaxRepairRounds = 3
	// RouteWorkers is how many legs are street routed at once.
	RouteWorkers = 4
)

// Runner is the runner half of a plan request.
type Runner struct {
	// FinishSeconds is the expected chip time.
	FinishSeconds float64 `json:"finishSeconds"`
	Split         float64 `json:"split"`
	// StartDelaySeconds is the wave/corral offset from the gun.
	StartDelaySeconds float64 `json:"startDelaySeconds"`
	Uncertainty       float64 `json:"uncertainty"`
}

// Spectator is the spectator half of a plan request.
type Spectator struct {
	Start geo.LatLon `json:"start"`
	// StartTimeSeconds is when the spectator sets off, relative to the gun
	// (negative means before the gun).
	StartTimeSeconds float64     `json:"startTimeSeconds"`
	Mode             travel.Mode `json:"mode"`
	// BufferSeconds defaults to 180.
	BufferSeconds *float64 `json:"bufferSeconds"`
	// RaceDay picks the transit timetable; empty means Sunday.
	RaceDay RaceDay `json:"raceDay"`
	// AvoidFerries keeps street routes off ferries.
	AvoidFerries bool `json:"avoidFerries"`
}

// RaceDay is the kind of day a race is run on, which decides the transit
// timetable: most marathons are on a Sunday, but Boston is on a Monday
// and the T runs its weekday schedule.
type RaceDay string

const (
	Sunday   RaceDay = "sunday"
	Saturday RaceDay = "saturday"
	Weekday  RaceDay = "weekday"
)

// RaceDays are the valid race days, in the order the UI lists them.
var RaceDays = []RaceDay{Sunday, Saturday, Weekday}

// label is the day as the routing line shows it: "Sunday schedule".
func (d RaceDay) label() string {
	if d == Weekday {
		return "weekday"
	}
	return strings.ToUpper(string(d[:1])) + string(d[1:])
}

func parseRaceDay(s RaceDay) (RaceDay, error) {
	if s == "" {
		return Sunday, nil
	}
	for _, d := range RaceDays {
		if s == d {
			return d, nil
		}
	}
	return "", fmt.Errorf("unknown race day %q (want sunday, saturday or weekday)", s)
}

// Params is the JSON "params" part of POST /api/plan.
type Params struct {
	Runner    Runner    `json:"runner"`
	Spectator Spectator `json:"spectator"`
	// CertifiedMeters, if set, rescales the GPX to the certified distance.
	CertifiedMeters float64 `json:"certifiedMeters"`
	// SpacingMeters between candidate spots; defaults to 300.
	SpacingMeters float64           `json:"spacingMeters"`
	Pinned        []geo.LatLon      `json:"pinned"`
	Excluded      []candidates.Area `json:"excluded"`
	Crossings     []geo.LatLon      `json:"crossings"`
	MileWeights   map[int]float64   `json:"mileWeights"`
	MaxStops      int               `json:"maxStops"`
	StopPenalty   float64           `json:"stopPenalty"`
}

// Response is the body of a successful POST /api/plan.
type Response struct {
	Course     Course       `json:"course"`
	Mode       travel.Mode  `json:"mode"`
	Candidates int          `json:"candidates"`
	Plan       planner.Plan `json:"plan"`
	// Routing says in words where travel times came from.
	Routing string `json:"routing"`
	// Legs[i] is how to get to Plan.Stops[i] from the previous stop (or
	// the start). Its steps' seconds sum to that stop's travel time.
	Legs [][]travel.Step `json:"legs"`
	// MissedPins are the pinned spots the plan does not visit, because
	// they are too far from the course or cannot be reached in time.
	MissedPins []geo.LatLon `json:"missedPins"`
	// Finish is when the runner is expected to finish (fastest, expected
	// and slowest, as for each stop's runner times). It is not a sighting.
	Finish PassTimes `json:"finish"`
	// NoTransit, set only when transit was asked for, says that no subway
	// or rail network covers the course or the spectator's start, so the
	// plan walks between spots instead (Mode is then walk).
	NoTransit string `json:"noTransit,omitempty"`
}

// PassTimes are when the runner reaches a point, in seconds since the gun.
type PassTimes struct {
	Fast     float64 `json:"tFast"`
	Expected float64 `json:"tExpected"`
	Slow     float64 `json:"tSlow"`
}

// Config is what the server knows beyond a single request.
type Config struct {
	// Networks are the subway systems available for transit mode, on
	// their Sunday timetable. The first that covers the course is used.
	Networks []travel.Network
	// DayNetworks are the same systems on other race days' timetables.
	// A day with none uses Networks.
	DayNetworks map[RaceDay][]travel.Network
	// Walk times walking, for walk mode and the walking parts of transit.
	// Nil means the straight-line stub. If it is a travel.Prefetcher it is
	// warmed with every point a plan may ask about before solving.
	Walk travel.Provider
	// Races are the preloaded courses; nil means the embedded ones.
	Races *races.Catalog
	// Streets, if set, routes the final plan's walking, biking and driving
	// legs street by street (see travel.Routed). Nil means estimates only.
	Streets travel.StreetRouter
}

// Course is the (possibly calibrated) course for drawing on the map.
type Course struct {
	Points []geo.LatLon `json:"points"`
	Length float64      `json:"length"`
}

// Handler returns the API routes, to be mounted at /api/.
func Handler(cfg Config) http.Handler {
	catalog := cfg.Races
	if catalog == nil {
		catalog = races.Default
	}
	g := newGate(MaxConcurrentPlans, MaxCrossingPlans, MaxQueuedPlans, QueueWait)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /api/plan", func(w http.ResponseWriter, r *http.Request) {
		handlePlan(w, r, cfg, g)
	})
	mux.HandleFunc("GET /api/races", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(catalog.List())
	})
	mux.HandleFunc("GET /api/races/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		data, ok := catalog.GPX(id)
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("unknown race %q", id))
			return
		}
		w.Header().Set("Content-Type", "application/gpx+xml")
		w.Write(data)
	})
	return mux
}

// handlePlan expects multipart/form-data with a "gpx" file and a "params"
// JSON field.
func handlePlan(w http.ResponseWriter, r *http.Request, cfg Config, g *gate) {
	leave, ok := g.join()
	if !ok {
		writeBusy(w)
		return
	}
	defer leave()
	r.Body = http.MaxBytesReader(w, r.Body, MaxUpload)
	if err := r.ParseMultipartForm(MaxUpload); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("expected multipart form with gpx and params: %w", err))
		return
	}
	var p Params
	if err := json.Unmarshal([]byte(r.FormValue("params")), &p); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("params: %w", err))
		return
	}
	crossings := len(p.Crossings) > 0
	release, ok := g.enter(r.Context(), crossings)
	if !ok {
		writeBusy(w)
		return
	}
	defer release()
	// The GPX is parsed only once a slot is free, since a large track takes
	// far more memory parsed than as raw bytes.
	f, _, err := r.FormFile("gpx")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("missing gpx file"))
		return
	}
	defer f.Close()
	track, err := gpx.Parse(f)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	timeout := PlanTimeout
	if crossings {
		timeout = CrossingPlanTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	resp, err := Plan(ctx, track, p, cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Plan runs the whole pipeline on a parsed track. When ctx ends, travel
// lookups stop and Plan returns an error.
func Plan(ctx context.Context, track *gpx.Track, p Params, cfg Config) (*Response, error) {
	mode, err := travel.ParseMode(string(p.Spectator.Mode))
	if err != nil {
		return nil, err
	}
	if err := checkLimits(track, p); err != nil {
		return nil, err
	}
	if p.CertifiedMeters > 0 {
		track.Calibrate(p.CertifiedMeters)
	}
	profile := pace.Profile{
		Distance:    track.Length(),
		Finish:      p.Runner.FinishSeconds,
		Split:       p.Runner.Split,
		StartDelay:  p.Runner.StartDelaySeconds,
		Uncertainty: p.Runner.Uncertainty,
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	buffer := 180.0
	if p.Spectator.BufferSeconds != nil {
		buffer = *p.Spectator.BufferSeconds
	}
	if buffer < 0 {
		return nil, errors.New("buffer cannot be negative")
	}
	day, err := parseRaceDay(p.Spectator.RaceDay)
	if err != nil {
		return nil, err
	}

	nodes := candidates.Generate(track, profile, candidates.Options{
		Spacing:     p.SpacingMeters,
		Pinned:      p.Pinned,
		Excluded:    p.Excluded,
		MileWeights: p.MileWeights,
	})
	if len(nodes) > MaxCandidates {
		return nil, fmt.Errorf("too many candidate spots (%d, max %d); increase spacing or pin fewer spots", len(nodes), MaxCandidates)
	}

	rt := chooseRouting(cfg, mode, day, p.Spectator.Start, track.Points)
	if rt.noTransit != "" {
		// A straight-line guess at transit speeds would promise trains that
		// may not exist, so the spectator walks between spots instead.
		mode = travel.Walk
	}
	var routed *travel.Routed
	if cfg.Streets != nil {
		routed = &travel.Routed{Estimate: rt.walk, Router: cfg.Streets, AvoidFerries: p.Spectator.AvoidFerries, Ctx: ctx}
	}
	if pf, ok := rt.walk.(travel.Prefetcher); ok {
		pts := []geo.LatLon{p.Spectator.Start}
		for _, n := range nodes {
			pts = append(pts, n.Pos)
		}
		if rt.net != nil {
			pts = append(pts, (&travel.Subway{Net: rt.net}).StationsNear(pts)...)
		}
		pts = append(pts, p.Crossings...)
		if len(pts) <= MaxPrefetchPoints {
			pf.Prefetch(pts)
		}
	}
	// build makes the provider chain. It is rebuilt for every solve, since
	// Subway and Cached remember times that street routing may change.
	build := func() travel.Provider {
		walk := rt.walk
		if routed != nil {
			walk = routed
		}
		if len(p.Crossings) > 0 {
			walk = travel.Closures{Inner: walk, Course: thin(track.Points, MaxClosurePoints), Crossings: p.Crossings}
		}
		provider := walk
		if rt.net != nil {
			provider = &travel.Subway{Net: rt.net, Walk: walk}
		}
		return &travel.Cached{Inner: provider}
	}
	solve := func(provider travel.Provider, nodes []candidates.Node) planner.Plan {
		travelFn := func(a, b geo.LatLon) (float64, bool) {
			if ctx.Err() != nil {
				return 0, false
			}
			return provider.Duration(a, b, mode)
		}
		return planner.Solve(
			planner.Start{Pos: p.Spectator.Start, Time: p.Spectator.StartTimeSeconds},
			nodes, travelFn,
			planner.Params{
				Buffer: buffer,
				// A true upper bound on straight-line speed (0 if the provider
				// cannot give one, which turns the prefilter off).
				MaxSpeed:    travel.MaxSpeed(provider, mode),
				MaxStops:    p.MaxStops,
				StopPenalty: p.StopPenalty,
			},
		)
	}

	// The hybrid: solve on estimates, street route the plan's legs, and
	// solve again with the routed times (and estimates scaled to match),
	// until a plan's legs are all routed already. After MaxRouteRounds the
	// solves keep to the current plan's spots, so the plan settles on
	// routed legs instead of moving to spots whose legs are estimates.
	// Routed is then frozen, so describing the legs below routes nothing
	// new and their steps match the times the plan was solved with.
	provider := build()
	plan := solve(provider, nodes)
	rounds := 0
	for routed != nil && rounds < MaxRouteRounds+MaxRepairRounds && ctx.Err() == nil {
		describeLegs(provider, p.Spectator.Start, plan.Stops, mode)
		if routed.TakeNew() == 0 {
			break
		}
		rounds++
		among := nodes
		if rounds > MaxRouteRounds {
			// Repair: keep to the plan's own spots, dropping any its routed
			// legs no longer reach in time.
			among = make([]candidates.Node, len(plan.Stops))
			for i, st := range plan.Stops {
				among[i] = st.Node
			}
		}
		provider = build()
		plan = solve(provider, among)
	}
	if ctx.Err() != nil {
		return nil, errors.New("planning took too long; try wider spacing or fewer crossings")
	}
	if routed != nil {
		routed.Freeze()
	}
	legs := describeLegs(provider, p.Spectator.Start, plan.Stops, mode)
	if routed != nil {
		st, left := routed.Stats(), estimatedLegs(legs)
		log.Printf("streets: %s plan, %d stops, %d re-plans: %d legs routed, %d no route, %d router busy; %d legs left as estimates",
			mode, len(plan.Stops), rounds, st.Routed, st.NoRoute, st.Unavailable, left)
		rt.label = streetsLabel(rt.label, st, left, cfg.Streets)
	}
	return &Response{
		Course:     Course{Points: track.Points, Length: track.Length()},
		Mode:       mode,
		Candidates: len(nodes),
		Plan:       plan,
		Routing:    rt.label,
		Legs:       legs,
		MissedPins: missedPins(p.Pinned, plan.Stops),
		Finish: PassTimes{
			Fast:     profile.Fast(profile.Distance),
			Expected: profile.Expected(profile.Distance),
			Slow:     profile.Slow(profile.Distance),
		},
		NoTransit: rt.noTransit,
	}, nil
}

// describeLegs breaks each stop's leg into steps, a few legs at a time
// since describing can street route them.
func describeLegs(provider travel.Provider, start geo.LatLon, stops []planner.Stop, mode travel.Mode) [][]travel.Step {
	legs := make([][]travel.Step, len(stops))
	sem := make(chan struct{}, RouteWorkers)
	var wg sync.WaitGroup
	prev := start
	for i, st := range stops {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, a, b geo.LatLon) {
			defer func() { <-sem; wg.Done() }()
			steps, ok := travel.Describe(provider, a, b, mode)
			if !ok {
				steps = []travel.Step{}
			}
			legs[i] = steps
		}(i, prev, st.Node.Pos)
		prev = st.Node.Pos
	}
	wg.Wait()
	return legs
}

// estimatedLegs counts the walking, biking and driving steps that have no
// street path (and go somewhere), so they are drawn as straight lines.
func estimatedLegs(legs [][]travel.Step) int {
	n := 0
	for _, leg := range legs {
		for _, st := range leg {
			switch st.Kind {
			case travel.StepWalk, travel.StepBike, travel.StepDrive:
				if len(st.Path) < 2 && st.FromPos != st.ToPos {
					n++
				}
			}
		}
	}
	return n
}

// streetsLabel rewrites the routing line once legs are street routed:
// "Walking: street routes from openrouteservice.org by HeiGIT" (the ORS
// terms ask for the credit), and how many legs are still estimates.
func streetsLabel(label string, st travel.RoutedStats, left int, router travel.StreetRouter) string {
	name := "the street router"
	if s, ok := router.(fmt.Stringer); ok {
		name = s.String()
	}
	if st.Routed == 0 {
		if st.Unavailable > 0 {
			return label + ". Street routing is unavailable right now, so legs are estimates"
		}
		return label
	}
	routes := "street routes from " + name
	note := strings.Replace(label, "straight-line estimate", routes, 1)
	if note == label {
		note = strings.Replace(label, "Straight-line estimate", strings.ToUpper(routes[:1])+routes[1:], 1)
	}
	if note == label {
		note = label + ". " + strings.ToUpper(routes[:1]) + routes[1:]
	}
	if left > 0 {
		why := ""
		if st.Unavailable > 0 {
			why = ", routing was busy"
		}
		what := fmt.Sprintf("%d legs are straight-line estimates", left)
		if left == 1 {
			what = "1 leg is a straight-line estimate"
		}
		note += " (" + what + why + ")"
	}
	return note
}

// missedPins returns the pins with no pinned stop within snapping distance.
func missedPins(pins []geo.LatLon, stops []planner.Stop) []geo.LatLon {
	missed := []geo.LatLon{}
	for _, pin := range pins {
		hit := false
		for _, st := range stops {
			if st.Node.Pinned && geo.Distance(pin, st.Node.Pos) <= candidates.DefaultSnapRadius+1 {
				hit = true
				break
			}
		}
		if !hit {
			missed = append(missed, pin)
		}
	}
	return missed
}

// checkLimits rejects requests big enough to exhaust memory or CPU, and
// coordinates that are not real places.
func checkLimits(track *gpx.Track, p Params) error {
	if n := len(track.Points); n > MaxPoints {
		return fmt.Errorf("course has %d points (max %d)", n, MaxPoints)
	}
	length := track.Length()
	if p.CertifiedMeters > 0 {
		length = p.CertifiedMeters
	}
	if length > MaxCourseMeters || track.Length() > MaxCourseMeters {
		return fmt.Errorf("course is longer than %d km", MaxCourseMeters/1000)
	}
	if p.SpacingMeters > 0 && p.SpacingMeters < MinSpacingMeters {
		return fmt.Errorf("spacing must be at least %d m", MinSpacingMeters)
	}
	if p.Runner.FinishSeconds > MaxFinishSeconds {
		return fmt.Errorf("finish time must be under %d hours", MaxFinishSeconds/3600)
	}
	if len(p.MileWeights) > MaxMileWeights {
		return fmt.Errorf("at most %d mile weights", MaxMileWeights)
	}
	if p.MaxStops < 0 || p.MaxStops > MaxStopsLimit {
		return fmt.Errorf("max stops must be between 0 (no limit) and %d", MaxStopsLimit)
	}
	if len(p.Pinned) > MaxMarks || len(p.Excluded) > MaxMarks || len(p.Crossings) > MaxMarks {
		return fmt.Errorf("at most %d pinned spots, excluded areas and crossings each", MaxMarks)
	}
	pts := []geo.LatLon{p.Spectator.Start}
	pts = append(pts, p.Pinned...)
	pts = append(pts, p.Crossings...)
	for _, a := range p.Excluded {
		pts = append(pts, a.Center)
	}
	for _, pt := range pts {
		if !gpx.ValidCoord(pt) {
			return fmt.Errorf("invalid coordinates (%v, %v)", pt.Lat, pt.Lon)
		}
	}
	return nil
}

// thin keeps at most max points of a polyline, evenly by index, always
// keeping the last.
func thin(pts []geo.LatLon, max int) []geo.LatLon {
	if len(pts) <= max {
		return pts
	}
	step := (len(pts) + max - 2) / (max - 1)
	var out []geo.LatLon
	for i := 0; i < len(pts)-1; i += step {
		out = append(out, pts[i])
	}
	return append(out, pts[len(pts)-1])
}

// routing is the travel setup chosen for one request.
type routing struct {
	// walk is the base provider before closures: the configured walker for
	// walk and transit modes, the straight-line stub otherwise.
	walk  travel.Provider
	net   travel.Network // nil unless transit runs on a subway
	label string
	// noTransit, in transit mode, says why no network can be used and the
	// plan walks instead.
	noTransit string
}

func chooseRouting(cfg Config, mode travel.Mode, day RaceDay, start geo.LatLon, course []geo.LatLon) routing {
	stub := travel.Haversine{}
	walk, walkName := cfg.Walk, "straight-line estimate"
	if walk == nil {
		walk = stub
	} else if s, ok := walk.(fmt.Stringer); ok {
		walkName = s.String()
	} else {
		walkName = "routed"
	}
	switch mode {
	case travel.Walk:
		return routing{walk: walk, label: "Walking: " + walkName}
	case travel.Transit:
		nets, schedule := cfg.DayNetworks[day], day
		if len(nets) == 0 {
			nets, schedule = cfg.Networks, Sunday
		}
		for _, n := range nets {
			if covers(n, start, course) {
				return routing{walk: walk, net: n, label: fmt.Sprintf("Subway: %s (%s schedule), walking: %s", n.Name(), schedule.label(), walkName)}
			}
		}
		why := "No transit data is available for this course: no subway or rail network the planner knows covers it."
		if len(nets) == 0 {
			why = "No transit data is available: the server has no subway or rail networks loaded."
		}
		return routing{walk: walk, label: "Walking: " + walkName, noTransit: why + " This plan walks between spots."}
	}
	return routing{walk: stub, label: "Straight-line estimate"}
}

// covers reports whether n serves the spectator's start or any part of the
// course. Point-to-point courses like Boston start far outside the subway,
// and legs there simply come out as walking.
func covers(n travel.Network, start geo.LatLon, course []geo.LatLon) bool {
	if n.Covers(start) {
		return true
	}
	for _, p := range course {
		if n.Covers(p) {
			return true
		}
	}
	return false
}

func writeBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	writeError(w, http.StatusServiceUnavailable, errors.New("server busy, try again in a few seconds"))
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
