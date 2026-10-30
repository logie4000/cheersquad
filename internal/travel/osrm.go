package travel

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/logie4000/cheersquad/internal/geo"
)

// OSRM times legs in one mode over the street network with an OSRM server
// built for that mode's profile (foot, bicycle or car); each OSRM server
// serves one profile. Only Mode is routed (the walking parts of transit are
// asked as Walk too); other modes, and every leg while the server is
// unreachable, use Fallback, so a plan always comes back.
type OSRM struct {
	// BaseURL is the server root, e.g. "http://osrm:5000".
	BaseURL string
	// Mode is the mode this server routes. Empty means Walk.
	Mode Mode
	// Profile is the OSRM profile name in request URLs. Empty means foot,
	// bicycle or car by Mode. (osrm-routed answers with its one dataset
	// whatever the name.)
	Profile string
	// Client is the HTTP client. Nil means one with a 10 s timeout.
	Client *http.Client
	// Fallback answers when OSRM cannot, and for modes other than Mode.
	Fallback Haversine
	// Factor multiplies OSRM's durations, which assume free-flowing streets
	// (race-day traffic for cars). Zero means 1.
	Factor float64
	// Overhead is seconds added to every routed leg: parking a car or
	// locking a bike and walking to the course. A leg detoured through a
	// crossing point is two routed legs and gets it twice.
	Overhead float64
	// SpeedBound is the speed bound in m/s, before Factor, reported by
	// MaxSpeed. It must exceed any speed the profile uses (OSRM foot is
	// 5 km/h, ~1.4 m/s). Zero means 2.0 for Walk, 8.0 for Bike and 40 for
	// Drive.
	SpeedBound float64
	// ChunkSize caps sources and destinations per /table request, so one
	// request has at most 2*ChunkSize locations. Zero means 50, which fits
	// OSRM's default --max-table-size of 100.
	ChunkSize int
	// Exclude is a comma-separated list of road classes to avoid, sent with
	// every request ("ferry"). The server's profile must make them
	// excludable (deploy/osrm/excludable-ferry.lua); if it doesn't, requests
	// go without it and the first refusal is logged.
	Exclude string
	// MaxSnap is how far in meters a point may be from the nearest street in
	// OSRM's data. OSRM snaps any point to its nearest street, however far,
	// so a point outside the area the data covers (another city) would get a
	// time between two unrelated streets; such legs use Fallback instead.
	// Zero means 1000.
	MaxSnap float64
	// MaxMemo caps cached pairs; the cache is shared by every request, so
	// when full it is cleared rather than growing without bound. Zero means
	// 2M pairs (roughly 200 MB).
	MaxMemo int

	// tables and routes count requests sent, for Requests.
	tables, routes atomic.Int64
	// noExclude is set once the server refuses Exclude.
	noExclude atomic.Bool

	mu        sync.Mutex
	memo      map[[2]geo.LatLon]osrmVal
	downUntil time.Time
	logged    bool
}

// errNoRoute is OSRM saying the points are not connected on foot.
var errNoRoute = errors.New("osrm: no route")

// errOutside is a point further than MaxSnap from OSRM's streets.
var errOutside = errors.New("osrm: point outside the routing data")

// osrmVal is a memoized OSRM answer. outside means a point was beyond
// MaxSnap, so the leg is Fallback's.
type osrmVal struct {
	s           float64
	ok, outside bool
}

func (o *OSRM) maxSnap() float64 {
	if o.MaxSnap <= 0 {
		return 1000
	}
	return o.MaxSnap
}

// waypoint is a location as OSRM snapped it; Distance is how far it moved.
type waypoint struct {
	Distance float64 `json:"distance"`
}

// osrmRetry is how long OSRM is skipped after a network error, so a down
// server costs one timeout per plan rather than one per pair.
const osrmRetry = time.Minute

func (o *OSRM) String() string {
	switch o.Exclude {
	case "":
		return fmt.Sprintf("OSRM %s routing", o.profile())
	case "ferry":
		return fmt.Sprintf("OSRM %s routing without ferries", o.profile())
	}
	return fmt.Sprintf("OSRM %s routing avoiding %s", o.profile(), o.Exclude)
}

func (o *OSRM) mode() Mode {
	if o.Mode == "" {
		return Walk
	}
	return o.Mode
}

func (o *OSRM) profile() string {
	if o.Profile != "" {
		return o.Profile
	}
	switch o.mode() {
	case Bike:
		return "bicycle"
	case Drive:
		return "car"
	}
	return "foot"
}

// adjust turns an OSRM duration into a leg time.
func (o *OSRM) adjust(s float64) float64 {
	f := o.Factor
	if f <= 0 {
		f = 1
	}
	return s*f + o.Overhead
}

func (o *OSRM) client() *http.Client {
	if o.Client == nil {
		return &http.Client{Timeout: 10 * time.Second}
	}
	return o.Client
}

func (o *OSRM) Duration(a, b geo.LatLon, m Mode) (float64, bool) {
	if m != o.mode() {
		return o.Fallback.Duration(a, b, m)
	}
	k := [2]geo.LatLon{a, b}
	o.mu.Lock()
	v, hit := o.memo[k]
	down := time.Now().Before(o.downUntil)
	o.mu.Unlock()
	if hit && v.outside {
		return o.Fallback.Duration(a, b, m)
	}
	if hit {
		return o.adjust(v.s), v.ok
	}
	if down {
		return o.Fallback.Duration(a, b, m)
	}
	s, _, err := o.route(a, b, false)
	switch {
	case err == nil:
		o.store(k, osrmVal{s: s, ok: true})
		return o.adjust(s), true
	case errors.Is(err, errNoRoute):
		o.store(k, osrmVal{})
		return 0, false
	case errors.Is(err, errOutside):
		o.store(k, osrmVal{outside: true})
		return o.Fallback.Duration(a, b, m)
	}
	o.fail(err)
	return o.Fallback.Duration(a, b, m)
}

func (o *OSRM) store(k [2]geo.LatLon, v osrmVal) {
	o.mu.Lock()
	o.makeRoom(1)
	o.memo[k] = v
	o.mu.Unlock()
}

// makeRoom clears the memo if adding n pairs would pass MaxMemo. The
// caller holds o.mu.
func (o *OSRM) makeRoom(n int) {
	limit := o.MaxMemo
	if limit <= 0 {
		limit = 2_000_000
	}
	if o.memo == nil || len(o.memo)+n > limit {
		o.memo = map[[2]geo.LatLon]osrmVal{}
	}
}

// fail logs the first error and pauses OSRM for a while.
func (o *OSRM) fail(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.downUntil = time.Now().Add(osrmRetry)
	if !o.logged {
		o.logged = true
		log.Printf("osrm %s: %v; falling back to straight-line estimates", o.profile(), err)
	}
}

func coords(ps []geo.LatLon) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = strconv.FormatFloat(p.Lon, 'f', 6, 64) + "," + strconv.FormatFloat(p.Lat, 'f', 6, 64)
	}
	return strings.Join(parts, ";")
}

// get fetches an OSRM service URL and decodes the body into out. OSRM
// answers NoRoute with a 400 and a JSON body, so the code field decides.
func (o *OSRM) get(service, path string, q url.Values, out any) error {
	if o.Exclude != "" && !o.noExclude.Load() {
		err := o.send(service, path, withExclude(q, o.Exclude), out)
		var bad errInvalid
		if !errors.As(err, &bad) || !strings.Contains(bad.message, "xclude") {
			return err
		}
		o.noExclude.Store(true)
		log.Printf("osrm %s: server refused exclude=%s (%s); routing without it. Rebuild with deploy/scripts/build-osrm.sh to support it", o.profile(), o.Exclude, bad.message)
	}
	return o.send(service, path, q, out)
}

// errInvalid is OSRM refusing a request parameter.
type errInvalid struct{ message string }

func (e errInvalid) Error() string { return "osrm: invalid value: " + e.message }

func withExclude(q url.Values, exclude string) url.Values {
	c := url.Values{}
	for k, v := range q {
		c[k] = v
	}
	c.Set("exclude", exclude)
	return c
}

// send makes one request; see get.
func (o *OSRM) send(service, path string, q url.Values, out any) error {
	if service == "table" {
		o.tables.Add(1)
	} else {
		o.routes.Add(1)
	}
	u := strings.TrimRight(o.BaseURL, "/") + "/" + service + "/v1/" + o.profile() + "/" + path + "?" + q.Encode()
	resp, err := o.client().Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var env struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	body := json.NewDecoder(resp.Body)
	var raw json.RawMessage
	if err := body.Decode(&raw); err != nil {
		return fmt.Errorf("osrm %s: status %d: %w", service, resp.StatusCode, err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("osrm %s: %w", service, err)
	}
	switch env.Code {
	case "Ok":
		return json.Unmarshal(raw, out)
	case "NoRoute":
		return errNoRoute
	case "InvalidValue":
		return errInvalid{env.Message}
	}
	return fmt.Errorf("osrm %s: status %d code %q: %s", service, resp.StatusCode, env.Code, env.Message)
}

// route asks OSRM for the fastest a->b route: its raw duration and, if
// path is true, its street geometry.
func (o *OSRM) route(a, b geo.LatLon, path bool) (float64, []geo.LatLon, error) {
	var r struct {
		Routes []struct {
			Duration float64 `json:"duration"`
			Geometry struct {
				Coordinates [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"routes"`
		Waypoints []waypoint `json:"waypoints"`
	}
	q := url.Values{"overview": {"false"}}
	if path {
		q = url.Values{"overview": {"full"}, "geometries": {"geojson"}}
	}
	if err := o.get("route", coords([]geo.LatLon{a, b}), q, &r); err != nil {
		return 0, nil, err
	}
	for _, w := range r.Waypoints {
		if w.Distance > o.maxSnap() {
			return 0, nil, errOutside
		}
	}
	if len(r.Routes) == 0 {
		return 0, nil, errNoRoute
	}
	var pts []geo.LatLon
	for _, c := range r.Routes[0].Geometry.Coordinates {
		pts = append(pts, geo.LatLon{Lat: c[1], Lon: c[0]})
	}
	return r.Routes[0].Duration, pts, nil
}

// Prefetch fills the cache with walking times between every ordered pair
// of points using /table requests of ChunkSize x ChunkSize, so planning
// makes a handful of requests instead of one per pair. On a network error
// it stops and leaves Duration to fall back.
func (o *OSRM) Prefetch(points []geo.LatLon) {
	seen := map[geo.LatLon]bool{}
	var ps []geo.LatLon
	for _, p := range points {
		if !seen[p] {
			seen[p] = true
			ps = append(ps, p)
		}
	}
	n := o.ChunkSize
	if n <= 0 {
		n = 50
	}
	for si := 0; si < len(ps); si += n {
		src := ps[si:min(si+n, len(ps))]
		for di := 0; di < len(ps); di += n {
			dst := ps[di:min(di+n, len(ps))]
			if err := o.table(src, dst, si == di); err != nil {
				o.fail(err)
				return
			}
		}
	}
}

// table fetches durations from every src to every dst. When same is true
// src and dst are the same slice and are sent once.
func (o *OSRM) table(src, dst []geo.LatLon, same bool) error {
	locs := src
	dstOff := 0
	if !same {
		locs = append(append([]geo.LatLon(nil), src...), dst...)
		dstOff = len(src)
	}
	idx := func(off, n int) string {
		s := make([]string, n)
		for i := range s {
			s[i] = strconv.Itoa(off + i)
		}
		return strings.Join(s, ";")
	}
	q := url.Values{"annotations": {"duration"}}
	if !same {
		q.Set("sources", idx(0, len(src)))
		q.Set("destinations", idx(dstOff, len(dst)))
	}
	var r struct {
		Durations    [][]*float64 `json:"durations"`
		Sources      []waypoint   `json:"sources"`
		Destinations []waypoint   `json:"destinations"`
	}
	if err := o.get("table", coords(locs), q, &r); err != nil {
		if errors.Is(err, errNoRoute) {
			return nil
		}
		return err
	}
	if len(r.Durations) != len(src) {
		return fmt.Errorf("osrm table: %d rows for %d sources", len(r.Durations), len(src))
	}
	outside := func(ws []waypoint, i int) bool {
		return i < len(ws) && ws[i].Distance > o.maxSnap()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.makeRoom(len(src) * len(dst))
	for i, row := range r.Durations {
		if len(row) != len(dst) {
			return fmt.Errorf("osrm table: row of %d for %d destinations", len(row), len(dst))
		}
		for j, d := range row {
			v := osrmVal{ok: d != nil}
			if d != nil {
				v.s = *d
			}
			if outside(r.Sources, i) || outside(r.Destinations, j) {
				v = osrmVal{outside: true}
			}
			o.memo[[2]geo.LatLon{src[i], dst[j]}] = v
		}
	}
	return nil
}

// Steps is a single step lasting Duration's time. For Mode it carries the
// street route OSRM takes as its Path, from a to b (OSRM snaps the ends to
// the nearest street, so a and b are added at the ends). Without a route,
// because OSRM is down or the leg came from Fallback, Path is empty and the
// map draws a straight line.
func (o *OSRM) Steps(a, b geo.LatLon, m Mode) ([]Step, bool) {
	s, ok := o.Duration(a, b, m)
	if !ok {
		return nil, false
	}
	st := Step{Kind: StepKindFor(m), FromPos: a, ToPos: b, Seconds: s}
	o.mu.Lock()
	down := time.Now().Before(o.downUntil)
	o.mu.Unlock()
	if m == o.mode() && !down && a != b {
		_, pts, err := o.route(a, b, true)
		switch {
		case err == nil && len(pts) > 0:
			st.Path = append(append([]geo.LatLon{a}, pts...), b)
		case err != nil && !errors.Is(err, errNoRoute) && !errors.Is(err, errOutside):
			o.fail(err)
		}
	}
	return []Step{st}, true
}

// MaxSpeed is SpeedBound / Factor for Mode and Fallback's bound otherwise.
// Overhead only makes legs slower, so it is left out. While OSRM is down
// legs come from Fallback, so its bound counts too.
func (o *OSRM) MaxSpeed(m Mode) float64 {
	if m != o.mode() {
		return o.Fallback.MaxSpeed(m)
	}
	b := o.SpeedBound
	if b <= 0 {
		switch m {
		case Bike:
			b = 8.0
		case Drive:
			b = 40
		default:
			b = 2.0
		}
	}
	f := o.Factor
	if f <= 0 {
		f = 1
	}
	return max(b/f, o.Fallback.MaxSpeed(m))
}

// Requests returns how many /table and /route requests have been sent so
// far, so callers can log what a plan cost.
func (o *OSRM) Requests() (tables, routes int64) {
	return o.tables.Load(), o.routes.Load()
}
