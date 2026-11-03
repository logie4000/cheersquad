package travel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/logie4000/cheersquad/internal/geo"
)

// StreetRoute is one street-by-street route between two points.
type StreetRoute struct {
	Seconds float64
	Meters  float64
	// Path is the route's geometry from start to end.
	Path []geo.LatLon
}

// StreetRouter finds street routes for single legs. It is too slow and
// rate limited to ask inside the planner's inner loop; Routed asks it only
// for the legs of a finished plan.
type StreetRouter interface {
	Route(ctx context.Context, a, b geo.LatLon, m Mode, avoidFerries bool) (StreetRoute, error)
}

// ErrNoStreetRoute means the router answered but found no route (a point
// too far from any road, or no connection without a ferry).
var ErrNoStreetRoute = errors.New("no street route")

// ErrRouterUnavailable means the router could not be asked: it is down,
// over its rate limit or daily quota, or rejected the key.
var ErrRouterUnavailable = errors.New("street routing unavailable")

// ORS routes legs with the OpenRouteService directions API
// (api.openrouteservice.org), and times many legs at once with its matrix
// API (see Times). The free plan allows 2,000 directions and 500 matrix
// requests a day, and 40 a minute of each; ORS stays under those itself,
// and pauses after errors, so callers fall back to estimates.
type ORS struct {
	// Key is the API key, sent in the Authorization header.
	Key string
	// BaseURL is the API root. Empty means https://api.openrouteservice.org.
	BaseURL string
	// Client is the HTTP client. Nil means one with a 10 s timeout.
	Client *http.Client
	// PerMinute and PerDay cap directions requests. Zero means 35 and
	// 1900, a little under the free plan's limits so other uses of the key
	// still work.
	PerMinute, PerDay int
	// MatrixPerMinute and MatrixPerDay cap matrix requests. Zero means 35
	// and 450.
	MatrixPerMinute, MatrixPerDay int
	// MaxMemo caps remembered routes, shared by every request; when full
	// the oldest tenth are forgotten. Zero means 10000.
	MaxMemo int
	// MaxTimes caps remembered matrix times the same way. Zero means
	// 300000.
	MaxTimes int

	mu     sync.Mutex
	memo   map[orsKey]orsVal
	times  map[orsKey]orsTime // matrix durations
	cache  *orsCache          // the file answers are saved to (OpenCache)
	dir    orsBudget          // directions requests
	mat    orsBudget          // matrix requests
	logged string             // last logged failure, so each is logged once
}

// orsBudget tracks one endpoint's requests against its limits.
type orsBudget struct {
	recent    []time.Time // request times in the last minute
	day       string      // UTC date of count
	count     int
	downUntil time.Time
}

type orsKey struct {
	a, b    geo.LatLon
	m       Mode
	noFerry bool
}

type orsVal struct {
	r   StreetRoute
	ok  bool  // false: ErrNoStreetRoute
	day int64 // when ORS answered, in days since the Unix epoch
}

type orsTime struct {
	s   float64 // seconds; NaN for no route
	day int64
}

// orsProfiles maps modes to ORS profiles. Transit has none.
var orsProfiles = map[Mode]string{
	Walk:  "foot-walking",
	Bike:  "cycling-regular",
	Drive: "driving-car",
}

// orsRetry is how long ORS is skipped after it fails, so an outage costs
// one timeout per plan rather than one per leg. After a 429 (too many
// requests) the pause is only orsRateBackoff, and requests wait it out.
// (Variables so tests can shorten them.)
var (
	orsRetry       = time.Minute
	orsRateBackoff = 10 * time.Second
	// orsMaxWait is the longest one request waits for a free slot in the
	// minute budget (or the end of a 429 pause), and only while the
	// plan's deadline leaves time to spare.
	orsMaxWait = 10 * time.Second
)

// orsStatusError is an HTTP error from ORS.
type orsStatusError struct {
	code int
	msg  string
}

func (e *orsStatusError) Error() string { return fmt.Sprintf("status %d: %s", e.code, e.msg) }

func (o *ORS) String() string { return "openrouteservice.org by HeiGIT" }

func (o *ORS) client() *http.Client {
	if o.Client == nil {
		return &http.Client{Timeout: 10 * time.Second}
	}
	return o.Client
}

func (o *ORS) Route(ctx context.Context, a, b geo.LatLon, m Mode, avoidFerries bool) (StreetRoute, error) {
	profile, ok := orsProfiles[m]
	if !ok {
		return StreetRoute{}, fmt.Errorf("ors: no profile for %s", m)
	}
	k := orsKey{a, b, m, avoidFerries}
	o.mu.Lock()
	v, hit := o.memo[k]
	o.mu.Unlock()
	if hit {
		if !v.ok {
			return StreetRoute{}, ErrNoStreetRoute
		}
		return v.r, nil
	}
	var r StreetRoute
	var err error
	for attempt := 0; ; attempt++ {
		if err := o.take(ctx, &o.dir); err != nil {
			return StreetRoute{}, err
		}
		r, err = o.request(ctx, profile, a, b, avoidFerries)
		var se *orsStatusError
		if attempt == 0 && errors.As(err, &se) && se.code == http.StatusTooManyRequests {
			o.backOff(&o.dir)
			continue // take waits out the pause if the deadline allows
		}
		break
	}
	switch {
	case err == nil:
		o.store(k, orsVal{r: r, ok: true})
		return r, nil
	case errors.Is(err, ErrNoStreetRoute):
		o.store(k, orsVal{})
		return StreetRoute{}, err
	case ctx.Err() != nil:
		return StreetRoute{}, ErrRouterUnavailable
	}
	o.fail(&o.dir, err)
	return StreetRoute{}, ErrRouterUnavailable
}

// take claims one request from the minute and day budgets. When the
// minute budget is spent, or ORS asked us to slow down, it waits for a
// slot, up to orsMaxWait and only if ctx's deadline leaves a few seconds
// after that for the request; otherwise the leg keeps its estimate.
func (o *ORS) take(ctx context.Context, b *orsBudget) error {
	perMin, perDay := o.PerMinute, o.PerDay
	if b == &o.mat {
		perMin, perDay = o.MatrixPerMinute, o.MatrixPerDay
		if perDay <= 0 {
			perDay = 450
		}
	}
	if perMin <= 0 {
		perMin = 35
	}
	if perDay <= 0 {
		perDay = 1900
	}
	for {
		wait, err := o.tryTake(b, perMin, perDay)
		if err != nil || wait == 0 {
			return err
		}
		if wait > orsMaxWait {
			return ErrRouterUnavailable
		}
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait+2*time.Second {
			return ErrRouterUnavailable
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return ErrRouterUnavailable
		case <-t.C:
		}
	}
}

// tryTake claims a request now, or says how long until one may be.
func (o *ORS) tryTake(b *orsBudget, perMin, perDay int) (time.Duration, error) {
	now := time.Now()
	o.mu.Lock()
	defer o.mu.Unlock()
	if d := now.UTC().Format("2006-01-02"); d != b.day {
		b.day, b.count = d, 0
	}
	if b.count >= perDay {
		o.logOnce(fmt.Sprintf("ors: daily budget of %d %s requests used; estimates until midnight UTC", perDay, o.label(b)))
		return 0, ErrRouterUnavailable
	}
	if now.Before(b.downUntil) {
		return b.downUntil.Sub(now), nil
	}
	keep := b.recent[:0]
	for _, t := range b.recent {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	b.recent = keep
	if len(b.recent) >= perMin {
		return b.recent[0].Add(time.Minute).Sub(now) + 10*time.Millisecond, nil
	}
	b.recent = append(b.recent, now)
	b.count++
	return 0, nil
}

func (o *ORS) label(b *orsBudget) string {
	if b == &o.mat {
		return "matrix"
	}
	return "directions"
}

// backOff pauses requests briefly after ORS said 429.
func (o *ORS) backOff(b *orsBudget) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if until := time.Now().Add(orsRateBackoff); until.After(b.downUntil) {
		b.downUntil = until
	}
	o.logOnce(fmt.Sprintf("ors: %s status 429 (too many requests); waiting a few seconds", o.label(b)))
}

func (o *ORS) store(k orsKey, v orsVal) {
	v.day = today()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.memo == nil {
		o.memo = map[orsKey]orsVal{}
	}
	o.memo[k] = v
	o.trimLocked()
	o.saveLocked(routeLine(k, v))
}

// fail pauses one endpoint for a while and logs the error once.
func (o *ORS) fail(b *orsBudget, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	b.downUntil = time.Now().Add(orsRetry)
	o.logOnce(fmt.Sprintf("ors: %s %v; estimates for a minute", o.label(b), err))
}

// logOnce logs msg unless it was the last thing logged. The caller holds o.mu.
func (o *ORS) logOnce(msg string) {
	if msg != o.logged {
		o.logged = msg
		log.Print(msg)
	}
}

type orsRequest struct {
	Coordinates [][2]float64 `json:"coordinates"`
	Options     *orsOptions  `json:"options,omitempty"`
}

type orsOptions struct {
	AvoidFeatures []string `json:"avoid_features"`
}

type orsResponse struct {
	Features []struct {
		Geometry struct {
			Coordinates [][]float64 `json:"coordinates"`
		} `json:"geometry"`
		Properties struct {
			Summary struct {
				Distance float64 `json:"distance"`
				Duration float64 `json:"duration"`
			} `json:"summary"`
		} `json:"properties"`
	} `json:"features"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ORS error codes that mean "no route" rather than a failure: 2009 route
// not found, 2010 a point not near any road, 2004 the route is too long
// for the profile.
var orsNoRoute = map[int]bool{2004: true, 2009: true, 2010: true}

func (o *ORS) request(ctx context.Context, profile string, a, b geo.LatLon, avoidFerries bool) (StreetRoute, error) {
	body := orsRequest{Coordinates: [][2]float64{{a.Lon, a.Lat}, {b.Lon, b.Lat}}}
	if avoidFerries {
		body.Options = &orsOptions{AvoidFeatures: []string{"ferries"}}
	}
	buf, _ := json.Marshal(body)
	base := o.BaseURL
	if base == "" {
		base = "https://api.openrouteservice.org"
	}
	u := strings.TrimRight(base, "/") + "/v2/directions/" + profile + "/geojson"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(buf))
	if err != nil {
		return StreetRoute{}, err
	}
	req.Header.Set("Authorization", o.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/geo+json, application/json")
	resp, err := o.client().Do(req)
	if err != nil {
		return StreetRoute{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return StreetRoute{}, err
	}
	var r orsResponse
	jsonErr := json.Unmarshal(raw, &r)
	if r.Error != nil && orsNoRoute[r.Error.Code] {
		return StreetRoute{}, ErrNoStreetRoute
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if r.Error != nil {
			msg = fmt.Sprintf("code %d: %s", r.Error.Code, r.Error.Message)
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return StreetRoute{}, &orsStatusError{resp.StatusCode, msg}
	}
	if jsonErr != nil {
		return StreetRoute{}, jsonErr
	}
	if len(r.Features) == 0 {
		return StreetRoute{}, ErrNoStreetRoute
	}
	f := r.Features[0]
	out := StreetRoute{Seconds: f.Properties.Summary.Duration, Meters: f.Properties.Summary.Distance}
	for _, c := range f.Geometry.Coordinates {
		if len(c) >= 2 {
			out.Path = append(out.Path, geo.LatLon{Lat: c[1], Lon: c[0]})
		}
	}
	return out, nil
}
