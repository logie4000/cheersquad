// Package api exposes the planner over HTTP. It is the edge where request
// units (clock times, JSON shapes) are converted to the core's seconds since
// the gun and meters.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/logie4000/cheersquad/internal/candidates"
	"github.com/logie4000/cheersquad/internal/geo"
	"github.com/logie4000/cheersquad/internal/gpx"
	"github.com/logie4000/cheersquad/internal/pace"
	"github.com/logie4000/cheersquad/internal/planner"
	"github.com/logie4000/cheersquad/internal/travel"
)

// MaxUpload caps the request body. Marathon GPX files with dense track
// points are typically 1-5 MB.
const MaxUpload = 20 << 20

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
}

// Config is what the server knows beyond a single request.
type Config struct {
	// Networks are the subway systems available for transit mode. The
	// first that covers the course is used.
	Networks []travel.Network
	// Walk times walking, for walk mode and the walking parts of transit.
	// Nil means the straight-line stub. If it is a travel.Prefetcher it is
	// warmed with every point a plan may ask about before solving.
	Walk travel.Provider
}

// Course is the (possibly calibrated) course for drawing on the map.
type Course struct {
	Points []geo.LatLon `json:"points"`
	Length float64      `json:"length"`
}

// Handler returns the API routes, to be mounted at /api/.
func Handler(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /api/plan", func(w http.ResponseWriter, r *http.Request) {
		handlePlan(w, r, cfg)
	})
	return mux
}

// handlePlan expects multipart/form-data with a "gpx" file and a "params"
// JSON field.
func handlePlan(w http.ResponseWriter, r *http.Request, cfg Config) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxUpload)
	if err := r.ParseMultipartForm(MaxUpload); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("expected multipart form with gpx and params: %w", err))
		return
	}
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
	var p Params
	if err := json.Unmarshal([]byte(r.FormValue("params")), &p); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("params: %w", err))
		return
	}
	resp, err := Plan(track, p, cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Plan runs the whole pipeline on a parsed track.
func Plan(track *gpx.Track, p Params, cfg Config) (*Response, error) {
	mode, err := travel.ParseMode(string(p.Spectator.Mode))
	if err != nil {
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

	nodes := candidates.Generate(track, profile, candidates.Options{
		Spacing:     p.SpacingMeters,
		Pinned:      p.Pinned,
		Excluded:    p.Excluded,
		MileWeights: p.MileWeights,
	})

	rt := chooseRouting(cfg, mode, p.Spectator.Start, track.Points)
	walk := rt.walk
	if len(p.Crossings) > 0 {
		walk = travel.Closures{Inner: walk, Course: track.Points, Crossings: p.Crossings}
	}
	var provider travel.Provider = walk
	if rt.net != nil {
		provider = &travel.Subway{Net: rt.net, Walk: walk}
	}
	if pf, ok := rt.walk.(travel.Prefetcher); ok {
		pts := []geo.LatLon{p.Spectator.Start}
		for _, n := range nodes {
			pts = append(pts, n.Pos)
		}
		if s, ok := provider.(*travel.Subway); ok {
			pts = append(pts, s.StationsNear(pts)...)
		}
		pf.Prefetch(append(pts, p.Crossings...))
	}
	provider = &travel.Cached{Inner: provider}
	travelFn := func(a, b geo.LatLon) (float64, bool) { return provider.Duration(a, b, mode) }

	plan := planner.Solve(
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
	legs := make([][]travel.Step, len(plan.Stops))
	prev := p.Spectator.Start
	for i, st := range plan.Stops {
		steps, ok := travel.Describe(provider, prev, st.Node.Pos, mode)
		if !ok {
			steps = []travel.Step{}
		}
		legs[i] = steps
		prev = st.Node.Pos
	}
	return &Response{
		Course:     Course{Points: track.Points, Length: track.Length()},
		Mode:       mode,
		Candidates: len(nodes),
		Plan:       plan,
		Routing:    rt.label,
		Legs:       legs,
	}, nil
}

// routing is the travel setup chosen for one request.
type routing struct {
	// walk is the base provider before closures: the configured walker for
	// walk and transit modes, the straight-line stub otherwise.
	walk  travel.Provider
	net   travel.Network // nil unless transit runs on a subway
	label string
}

func chooseRouting(cfg Config, mode travel.Mode, start geo.LatLon, course []geo.LatLon) routing {
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
		for _, n := range cfg.Networks {
			if covers(n, start, course) {
				return routing{walk: walk, net: n, label: fmt.Sprintf("Subway: %s, walking: %s", n.Name(), walkName)}
			}
		}
		why := "no subway data covers this course"
		if len(cfg.Networks) == 0 {
			why = "no subway data loaded"
		}
		return routing{walk: stub, label: "Straight-line estimate (" + why + ")"}
	}
	return routing{walk: stub, label: "Straight-line estimate"}
}

// covers reports whether n serves the spectator's start and at least half
// of the course.
func covers(n travel.Network, start geo.LatLon, course []geo.LatLon) bool {
	if !n.Covers(start) {
		return false
	}
	in := 0
	for _, p := range course {
		if n.Covers(p) {
			in++
		}
	}
	return 2*in >= len(course)
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
