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
	"time"

	"github.com/logie4000/cheersquad/internal/geo"
)

// OSRM times walking legs with an OSRM server's foot profile. Only mode Walk
// is routed (the walking parts of transit are asked as Walk too); other
// modes, and every leg while the server is unreachable, use Fallback, so a
// plan always comes back.
type OSRM struct {
	// BaseURL is the server root, e.g. "http://osrm:5000".
	BaseURL string
	// Profile is the OSRM profile name. Empty means "foot".
	Profile string
	// Client is the HTTP client. Nil means one with a 10 s timeout.
	Client *http.Client
	// Fallback answers when OSRM cannot, and for modes other than Walk.
	Fallback Haversine
	// SpeedBound is the walking speed bound in m/s reported by MaxSpeed. It
	// must exceed the profile's speed (OSRM foot is 5 km/h, ~1.4 m/s).
	// Zero means 2.0.
	SpeedBound float64
	// ChunkSize caps sources and destinations per /table request, so one
	// request has at most 2*ChunkSize locations. Zero means 50, which fits
	// OSRM's default --max-table-size of 100.
	ChunkSize int
	// MaxMemo caps cached pairs; the cache is shared by every request, so
	// when full it is cleared rather than growing without bound. Zero means
	// 2M pairs (roughly 200 MB).
	MaxMemo int

	mu        sync.Mutex
	memo      map[[2]geo.LatLon]cacheVal
	downUntil time.Time
	logged    bool
}

// errNoRoute is OSRM saying the points are not connected on foot.
var errNoRoute = errors.New("osrm: no route")

// osrmRetry is how long OSRM is skipped after a network error, so a down
// server costs one timeout per plan rather than one per pair.
const osrmRetry = time.Minute

func (o *OSRM) String() string {
	return fmt.Sprintf("OSRM %s routing", o.profile())
}

func (o *OSRM) profile() string {
	if o.Profile == "" {
		return "foot"
	}
	return o.Profile
}

func (o *OSRM) client() *http.Client {
	if o.Client == nil {
		return &http.Client{Timeout: 10 * time.Second}
	}
	return o.Client
}

func (o *OSRM) Duration(a, b geo.LatLon, m Mode) (float64, bool) {
	if m != Walk {
		return o.Fallback.Duration(a, b, m)
	}
	k := [2]geo.LatLon{a, b}
	o.mu.Lock()
	v, hit := o.memo[k]
	down := time.Now().Before(o.downUntil)
	o.mu.Unlock()
	if hit {
		return v.s, v.ok
	}
	if down {
		return o.Fallback.Duration(a, b, m)
	}
	s, err := o.route(a, b)
	switch {
	case err == nil:
		o.store(k, cacheVal{s, true})
		return s, true
	case errors.Is(err, errNoRoute):
		o.store(k, cacheVal{0, false})
		return 0, false
	}
	o.fail(err)
	return o.Fallback.Duration(a, b, m)
}

func (o *OSRM) store(k [2]geo.LatLon, v cacheVal) {
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
		o.memo = map[[2]geo.LatLon]cacheVal{}
	}
}

// fail logs the first error and pauses OSRM for a while.
func (o *OSRM) fail(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.downUntil = time.Now().Add(osrmRetry)
	if !o.logged {
		o.logged = true
		log.Printf("osrm: %v; falling back to straight-line walking estimates", err)
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
	}
	return fmt.Errorf("osrm %s: status %d code %q: %s", service, resp.StatusCode, env.Code, env.Message)
}

func (o *OSRM) route(a, b geo.LatLon) (float64, error) {
	var r struct {
		Routes []struct {
			Duration float64 `json:"duration"`
		} `json:"routes"`
	}
	if err := o.get("route", coords([]geo.LatLon{a, b}), url.Values{"overview": {"false"}}, &r); err != nil {
		return 0, err
	}
	if len(r.Routes) == 0 {
		return 0, errNoRoute
	}
	return r.Routes[0].Duration, nil
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
		Durations [][]*float64 `json:"durations"`
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
	o.mu.Lock()
	defer o.mu.Unlock()
	o.makeRoom(len(src) * len(dst))
	for i, row := range r.Durations {
		if len(row) != len(dst) {
			return fmt.Errorf("osrm table: row of %d for %d destinations", len(row), len(dst))
		}
		for j, d := range row {
			v := cacheVal{ok: d != nil}
			if d != nil {
				v.s = *d
			}
			o.memo[[2]geo.LatLon{src[i], dst[j]}] = v
		}
	}
	return nil
}

// Steps is a single step: OSRM is asked for durations only, not paths.
func (o *OSRM) Steps(a, b geo.LatLon, m Mode) ([]Step, bool) {
	s, ok := o.Duration(a, b, m)
	if !ok {
		return nil, false
	}
	return []Step{{Kind: StepKindFor(m), FromPos: a, ToPos: b, Seconds: s}}, true
}

// MaxSpeed is SpeedBound for Walk and Fallback's bound otherwise. While
// OSRM is down Walk legs come from Fallback, whose walking bound (1.3 m/s by
// default) is below SpeedBound's default.
func (o *OSRM) MaxSpeed(m Mode) float64 {
	if m != Walk {
		return o.Fallback.MaxSpeed(m)
	}
	b := o.SpeedBound
	if b <= 0 {
		b = 2.0
	}
	return max(b, o.Fallback.MaxSpeed(Walk))
}
