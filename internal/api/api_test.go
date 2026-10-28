package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/logie4000/cheersquad/internal/geo"
	"github.com/logie4000/cheersquad/internal/gpx"
	"github.com/logie4000/cheersquad/internal/races"
	"github.com/logie4000/cheersquad/internal/travel"
)

// straightGPX is roughly 10 km due north from (0, 0).
var straightGPX = fmt.Sprintf(`<gpx><trk><trkseg>
<trkpt lat="0" lon="0"/><trkpt lat="%f" lon="0"/>
</trkseg></trk></gpx>`, 10000/111194.93)

func request(t *testing.T, gpxBody, params string) *httptest.ResponseRecorder {
	return requestWith(t, Config{}, gpxBody, params)
}

func requestWith(t *testing.T, cfg Config, gpxBody, params string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if gpxBody != "" {
		fw, _ := mw.CreateFormFile("gpx", "course.gpx")
		fw.Write([]byte(gpxBody))
	}
	mw.WriteField("params", params)
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/plan", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	Handler(cfg).ServeHTTP(rec, req)
	return rec
}

func TestPlanEndToEnd(t *testing.T) {
	params := `{
		"runner": {"finishSeconds": 3000},
		"spectator": {"start": {"lat": 0, "lon": 0}, "mode": "drive", "bufferSeconds": 60, "startTimeSeconds": -600},
		"certifiedMeters": 10000,
		"spacingMeters": 1000
	}`
	rec := request(t, straightGPX, params)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Course.Length != 10000 || resp.Candidates != 11 || resp.Mode != "drive" {
		t.Fatalf("unexpected response: length %f candidates %d mode %s", resp.Course.Length, resp.Candidates, resp.Mode)
	}
	if resp.Routing != "Straight-line estimate" || len(resp.Legs) != len(resp.Plan.Stops) {
		t.Fatalf("routing %q, %d legs for %d stops", resp.Routing, len(resp.Legs), len(resp.Plan.Stops))
	}
	if resp.Plan.Sightings < 2 {
		t.Fatalf("a driver (about 4.2 m/s door to door) should leapfrog the runner, got %d sightings", resp.Plan.Sightings)
	}
	for i, s := range resp.Plan.Stops {
		if s.Wait < 60-1e-6 {
			t.Fatalf("stop at %f has wait %f below buffer", s.Node.Dist, s.Wait)
		}
		if leg := resp.Legs[i]; len(leg) != 1 || leg[0].Kind != travel.StepDrive || math.Abs(leg[0].Seconds-s.Travel) > 1e-6 {
			t.Fatalf("leg %d: %+v for travel %f", i, leg, s.Travel)
		}
	}
}

func TestPlanRejectsBadInput(t *testing.T) {
	good := `{"runner": {"finishSeconds": 3000}, "spectator": {"mode": "walk"}}`
	cases := map[string]struct{ gpx, params string }{
		"missing gpx":    {"", good},
		"bad gpx":        {"<gpx/>", good},
		"bad json":       {straightGPX, `{`},
		"bad mode":       {straightGPX, `{"runner": {"finishSeconds": 3000}, "spectator": {"mode": "fly"}}`},
		"no finish time": {straightGPX, `{"spectator": {"mode": "walk"}}`},
		"bad coords":     {`<gpx><trk><trkseg><trkpt lat="0" lon="0"/><trkpt lat="NaN" lon="0"/></trkseg></trk></gpx>`, good},
		// Roughly 111 km due north.
		"too long":       {`<gpx><trk><trkseg><trkpt lat="0" lon="0"/><trkpt lat="1" lon="0"/></trkseg></trk></gpx>`, good},
		"certified long": {straightGPX, `{"runner": {"finishSeconds": 3000}, "spectator": {"mode": "walk"}, "certifiedMeters": 1e9}`},
		"tiny spacing":   {straightGPX, `{"runner": {"finishSeconds": 3000}, "spectator": {"mode": "walk"}, "spacingMeters": 0.01}`},
		"many stops":     {straightGPX, `{"runner": {"finishSeconds": 3000}, "spectator": {"mode": "walk"}, "maxStops": 1000000}`},
		"slow finish":    {straightGPX, `{"runner": {"finishSeconds": 1e9}, "spectator": {"mode": "walk"}}`},
		"many weights":   {straightGPX, `{"runner": {"finishSeconds": 3000}, "spectator": {"mode": "walk"}, "mileWeights": {` + mileWeights(MaxMileWeights+1) + `}}`},
		"negative stops": {straightGPX, `{"runner": {"finishSeconds": 3000}, "spectator": {"mode": "walk"}, "maxStops": -1}`},
		"bad start":      {straightGPX, `{"runner": {"finishSeconds": 3000}, "spectator": {"mode": "walk", "start": {"lat": 200, "lon": 0}}}`},
		"too many pins":  {straightGPX, `{"runner": {"finishSeconds": 3000}, "spectator": {"mode": "walk"}, "pinned": [` + strings.Repeat(`{"lat":0,"lon":0},`, MaxMarks) + `{"lat":0,"lon":0}]}`},
	}
	for name, c := range cases {
		rec := request(t, c.gpx, c.params)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "error") {
			t.Errorf("%s: status %d body %s", name, rec.Code, rec.Body)
		}
	}
}

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(Config{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func racesHandler(t *testing.T, fsys fstest.MapFS) http.Handler {
	t.Helper()
	c, err := races.NewCatalog(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return Handler(Config{Races: c})
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestRacesList(t *testing.T) {
	h := racesHandler(t, fstest.MapFS{
		"b.gpx": {Data: []byte(`<gpx><metadata><name>Beta Marathon</name></metadata></gpx>`)},
		"a.gpx": {Data: []byte(straightGPX)},
	})
	rec := get(h, "/api/races")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if got, want := strings.TrimSpace(rec.Body.String()), `[{"id":"a","name":"a"},{"id":"b","name":"Beta Marathon"}]`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}

	empty := get(racesHandler(t, fstest.MapFS{}), "/api/races")
	if got := strings.TrimSpace(empty.Body.String()); got != "[]" {
		t.Fatalf("empty catalog should list [], got %s", got)
	}
}

func TestRaceGPX(t *testing.T) {
	h := racesHandler(t, fstest.MapFS{"a.gpx": {Data: []byte(straightGPX)}})
	rec := get(h, "/api/races/a")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/gpx+xml" || rec.Body.String() != straightGPX {
		t.Fatalf("status %d type %q body %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	for _, path := range []string{"/api/races/missing", "/api/races/a.gpx", "/api/races/..%2Fa"} {
		rec := get(h, path)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: status %d body %s", path, rec.Code, rec.Body)
		}
	}
}

// courseNet is a fake subway with a station every 0.03 degrees (about
// 3.3 km) just east of straightGPX. Each hop takes 120 s plus 60 s to get
// into the system.
// With minLat set it only covers points at or north of it.
type courseNet struct {
	covers bool
	minLat float64
}

func (courseNet) Name() string { return "Test Metro" }

func (courseNet) Stations() []travel.Station {
	var ss []travel.Station
	for i := 0; i < 4; i++ {
		ss = append(ss, travel.Station{ID: fmt.Sprint(i), Name: fmt.Sprintf("Stop %d", i), Pos: geo.LatLon{Lat: 0.03 * float64(i), Lon: 0.003}})
	}
	return ss
}

func (courseNet) Time(i, j int) (float64, bool) {
	return 60 + 120*math.Abs(float64(i-j)), true
}

func (courseNet) Rides(i, j int) []travel.Ride {
	return []travel.Ride{{Line: "1", Color: "EE352E", From: i, To: j, Seconds: 120 * math.Abs(float64(i-j))}}
}

func (courseNet) MaxSpeed() float64 { return 30 }

func (n courseNet) Covers(p geo.LatLon) bool {
	return n.covers || (n.minLat > 0 && p.Lat >= n.minLat)
}

// prefetchWalk is the straight-line walker that records Prefetch calls.
type prefetchWalk struct {
	travel.Haversine
	points []geo.LatLon
}

func (p *prefetchWalk) Prefetch(ps []geo.LatLon) { p.points = append(p.points, ps...) }

func planTransit(t *testing.T, cfg Config) Response {
	t.Helper()
	params := `{
		"runner": {"finishSeconds": 3000},
		"spectator": {"start": {"lat": 0, "lon": 0.003}, "mode": "transit", "bufferSeconds": 60, "startTimeSeconds": -600},
		"certifiedMeters": 10000,
		"spacingMeters": 1000
	}`
	rec := requestWith(t, cfg, straightGPX, params)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Legs) != len(resp.Plan.Stops) {
		t.Fatalf("%d legs for %d stops", len(resp.Legs), len(resp.Plan.Stops))
	}
	for i, leg := range resp.Legs {
		sum := 0.0
		for _, s := range leg {
			sum += s.Seconds
		}
		if math.Abs(sum-resp.Plan.Stops[i].Travel) > 1e-6 {
			t.Fatalf("leg %d steps sum %f, travel %f", i, sum, resp.Plan.Stops[i].Travel)
		}
	}
	return resp
}

func TestPlanTransitUsesSubway(t *testing.T) {
	walk := &prefetchWalk{}
	resp := planTransit(t, Config{Networks: []travel.Network{courseNet{covers: true}}, Walk: walk})
	if !strings.Contains(resp.Routing, "Test Metro") {
		t.Fatalf("routing %q should name the network", resp.Routing)
	}
	rode := false
	for _, leg := range resp.Legs {
		for _, s := range leg {
			if s.Kind == travel.StepSubway && s.Line == "1" && s.From != "" && s.To != "" {
				rode = true
			}
		}
	}
	if !rode {
		t.Fatalf("expected a subway step to keep up with a 3.3 m/s runner: %+v", resp.Legs)
	}
	// Walking alone (about 1 m/s) never catches the runner again after the
	// first sighting, so a second one needs the train.
	if resp.Plan.Sightings < 2 {
		t.Fatalf("the subway should leapfrog the runner, got %d sightings: %+v", resp.Plan.Sightings, resp.Plan.Stops)
	}
	// Start, 11 candidates, and the stations near them.
	if len(walk.points) < 1+11+4 {
		t.Fatalf("prefetched %d points", len(walk.points))
	}
}

// A point-to-point course that only reaches the subway late on (Boston)
// still gets subway routing.
func TestPlanTransitUsesSubwayForPartialCoverage(t *testing.T) {
	resp := planTransit(t, Config{Networks: []travel.Network{courseNet{minLat: 0.07}}})
	if !strings.Contains(resp.Routing, "Test Metro") {
		t.Fatalf("routing %q should name the network", resp.Routing)
	}
}

func TestPlanTransitFallsBackWithoutCoverage(t *testing.T) {
	resp := planTransit(t, Config{Networks: []travel.Network{courseNet{covers: false}}})
	if !strings.Contains(resp.Routing, "no subway data covers") {
		t.Fatalf("routing %q", resp.Routing)
	}
	for _, leg := range resp.Legs {
		if len(leg) != 1 || leg[0].Kind != travel.StepTransit {
			t.Fatalf("fallback legs should be one straight-line transit step: %+v", leg)
		}
	}
	resp = planTransit(t, Config{})
	if !strings.Contains(resp.Routing, "no subway data loaded") {
		t.Fatalf("routing %q", resp.Routing)
	}
}

// Boston runs on a Monday: the weekday network is used when asked for, and
// Sunday's otherwise.
func TestPlanTransitRaceDay(t *testing.T) {
	cfg := Config{
		Networks:    []travel.Network{courseNet{covers: true}},
		DayNetworks: map[RaceDay][]travel.Network{Weekday: {weekdayNet{courseNet{covers: true}}}},
	}
	plan := func(day string) (*Response, error) {
		var p Params
		p.Runner.FinishSeconds = 4 * 3600
		p.Spectator.Mode = travel.Transit
		p.Spectator.RaceDay = RaceDay(day)
		track, err := gpx.Parse(strings.NewReader(straightGPX))
		if err != nil {
			t.Fatal(err)
		}
		return Plan(context.Background(), track, p, cfg)
	}
	for day, want := range map[string]string{
		"":         "Test Metro (Sunday schedule)",
		"saturday": "Test Metro (Sunday schedule)",
		"weekday":  "Weekday Metro (weekday schedule)",
	} {
		resp, err := plan(day)
		if err != nil {
			t.Fatalf("%q: %v", day, err)
		}
		if !strings.Contains(resp.Routing, want) {
			t.Errorf("%q: routing %q, want %q", day, resp.Routing, want)
		}
	}
	if _, err := plan("monday"); err == nil {
		t.Error("an unknown race day should be rejected")
	}
}

type weekdayNet struct{ courseNet }

func (weekdayNet) Name() string { return "Weekday Metro" }

func mileWeights(n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"%d": 1`, i)
	}
	return b.String()
}

func TestThin(t *testing.T) {
	var pts []geo.LatLon
	for i := range 10_001 {
		pts = append(pts, geo.LatLon{Lat: float64(i) / 1e4})
	}
	got := thin(pts, 2000)
	if len(got) > 2000 || got[0] != pts[0] || got[len(got)-1] != pts[len(pts)-1] {
		t.Fatalf("thin: %d points, ends %v %v", len(got), got[0], got[len(got)-1])
	}
	if short := pts[:5]; len(thin(short, 2000)) != 5 {
		t.Fatal("thin changed a short course")
	}
}

func TestPlanStopsWhenContextEnds(t *testing.T) {
	track, err := gpx.Parse(strings.NewReader(straightGPX))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := Params{Runner: Runner{FinishSeconds: 3000}, Spectator: Spectator{Mode: travel.Walk}}
	if _, err := Plan(ctx, track, p, Config{}); err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("got %v, want timeout error", err)
	}
}
