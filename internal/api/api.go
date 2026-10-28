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
	"github.com/logie4000/cheersquad/internal/races"
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
}

// Course is the (possibly calibrated) course for drawing on the map.
type Course struct {
	Points []geo.LatLon `json:"points"`
	Length float64      `json:"length"`
}

// Handler returns the API routes, to be mounted at /api/.
func Handler() http.Handler { return newHandler(races.Default) }

func newHandler(catalog *races.Catalog) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /api/plan", handlePlan)
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
func handlePlan(w http.ResponseWriter, r *http.Request) {
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
	resp, err := Plan(track, p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Plan runs the whole pipeline on a parsed track.
func Plan(track *gpx.Track, p Params) (*Response, error) {
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

	stub := travel.Haversine{}
	var provider travel.Provider = stub
	if len(p.Crossings) > 0 {
		provider = travel.Closures{Inner: stub, Course: track.Points, Crossings: p.Crossings}
	}
	provider = &travel.Cached{Inner: provider}
	travelFn := func(a, b geo.LatLon) (float64, bool) { return provider.Duration(a, b, mode) }

	plan := planner.Solve(
		planner.Start{Pos: p.Spectator.Start, Time: p.Spectator.StartTimeSeconds},
		nodes, travelFn,
		planner.Params{
			Buffer: buffer,
			// The stub's detour factor is at least 1, so straight line at
			// the mode's speed is a true lower bound on travel time.
			MaxSpeed:    stub.Speed(mode),
			MaxStops:    p.MaxStops,
			StopPenalty: p.StopPenalty,
		},
	)
	return &Response{
		Course:     Course{Points: track.Points, Length: track.Length()},
		Mode:       mode,
		Candidates: len(nodes),
		Plan:       plan,
	}, nil
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
