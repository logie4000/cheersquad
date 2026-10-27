package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// straightGPX is roughly 10 km due north from (0, 0).
var straightGPX = fmt.Sprintf(`<gpx><trk><trkseg>
<trkpt lat="0" lon="0"/><trkpt lat="%f" lon="0"/>
</trkseg></trk></gpx>`, 10000/111194.93)

func request(t *testing.T, gpxBody, params string) *httptest.ResponseRecorder {
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
	Handler().ServeHTTP(rec, req)
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
	if resp.Plan.Sightings < 2 {
		t.Fatalf("a driver (about 4.2 m/s door to door) should leapfrog the runner, got %d sightings", resp.Plan.Sightings)
	}
	for _, s := range resp.Plan.Stops {
		if s.Wait < 60-1e-6 {
			t.Fatalf("stop at %f has wait %f below buffer", s.Node.Dist, s.Wait)
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
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}
