package travel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/logie4000/cheersquad/internal/geo"
)

// fakeORS answers directions at 1 m/s along a two-segment path via the
// midpoint, and "not near a road" (2010) for anything touching island. It
// counts requests and rejects any without the key.
func fakeORS(t *testing.T, hits *int32, status *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if s := atomic.LoadInt32(status); s != 0 {
			w.WriteHeader(int(s))
			w.Write([]byte(`{"error":{"code":0,"message":"rate limit"}}`))
			return
		}
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "k3y" {
			t.Errorf("%s with auth %q", r.Method, r.Header.Get("Authorization"))
		}
		if !strings.HasPrefix(r.URL.Path, "/v2/directions/") || !strings.HasSuffix(r.URL.Path, "/geojson") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var req orsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Coordinates) != 2 {
			t.Errorf("bad body: %v %+v", err, req)
		}
		a := geo.LatLon{Lat: req.Coordinates[0][1], Lon: req.Coordinates[0][0]}
		b := geo.LatLon{Lat: req.Coordinates[1][1], Lon: req.Coordinates[1][0]}
		if a == island || b == island {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":2010,"message":"Could not find routable point"}}`))
			return
		}
		ferry := req.Options != nil && len(req.Options.AvoidFeatures) == 1 && req.Options.AvoidFeatures[0] == "ferries"
		d := geo.Distance(a, b)
		if ferry {
			d *= 2 // the long way round
		}
		mid := []float64{(a.Lon + b.Lon) / 2, (a.Lat+b.Lat)/2 + 0.001}
		json.NewEncoder(w).Encode(map[string]any{
			"type": "FeatureCollection",
			"features": []any{map[string]any{
				"geometry":   map[string]any{"type": "LineString", "coordinates": [][]float64{{a.Lon, a.Lat}, mid, {b.Lon, b.Lat}}},
				"properties": map[string]any{"summary": map[string]any{"distance": d, "duration": d}},
			}},
		})
	}))
}

func TestORSRoute(t *testing.T) {
	var hits, status int32
	srv := fakeORS(t, &hits, &status)
	defer srv.Close()
	o := &ORS{Key: "k3y", BaseURL: srv.URL}
	ctx := context.Background()
	a, b := geo.LatLon{Lat: 0, Lon: 0}, geo.LatLon{Lat: 0.01, Lon: 0}
	r, err := o.Route(ctx, a, b, Walk, false)
	if err != nil {
		t.Fatal(err)
	}
	if d := geo.Distance(a, b); r.Seconds != d || r.Meters != d || len(r.Path) != 3 || r.Path[0] != a || r.Path[2] != b {
		t.Fatalf("route %+v", r)
	}
	if _, err := o.Route(ctx, a, b, Walk, false); err != nil || hits != 1 {
		t.Fatalf("repeat should come from the memo: err %v, %d requests", err, hits)
	}
	f, err := o.Route(ctx, a, b, Walk, true)
	if err != nil || f.Seconds != 2*r.Seconds || hits != 2 {
		t.Fatalf("avoiding ferries: %+v %v after %d requests", f, err, hits)
	}
	if _, err := o.Route(ctx, a, island, Bike, false); !errors.Is(err, ErrNoStreetRoute) {
		t.Fatalf("island: %v", err)
	}
	if _, err := o.Route(ctx, a, island, Bike, false); !errors.Is(err, ErrNoStreetRoute) || hits != 3 {
		t.Fatalf("no route should be remembered: %v after %d requests", err, hits)
	}
	if _, err := o.Route(ctx, a, b, Transit, false); err == nil {
		t.Fatal("transit has no ORS profile")
	}
}

// A failure pauses ORS, so the next legs fall back at once.
func TestORSPausesAfterFailure(t *testing.T) {
	var hits int32
	status := int32(http.StatusBadGateway)
	srv := fakeORS(t, &hits, &status)
	defer srv.Close()
	o := &ORS{Key: "k3y", BaseURL: srv.URL}
	a := geo.LatLon{Lat: 0, Lon: 0}
	for i := 0; i < 3; i++ {
		b := geo.LatLon{Lat: 0.01 * float64(i+1), Lon: 0}
		if _, err := o.Route(context.Background(), a, b, Drive, false); !errors.Is(err, ErrRouterUnavailable) {
			t.Fatalf("leg %d: %v", i, err)
		}
	}
	if hits != 1 {
		t.Fatalf("%d requests, want 1 before pausing", hits)
	}
}

// A 429 pauses briefly and the request is tried again, if the deadline
// leaves time for it.
func TestORSWaitsOutRateLimit(t *testing.T) {
	defer func(b time.Duration) { orsRateBackoff = b }(orsRateBackoff)
	orsRateBackoff = 50 * time.Millisecond
	var hits int32
	status := int32(http.StatusTooManyRequests)
	srv := fakeORS(t, &hits, &status)
	defer srv.Close()
	o := &ORS{Key: "k3y", BaseURL: srv.URL}
	a, b := geo.LatLon{}, geo.LatLon{Lat: 0.01}
	go func() {
		time.Sleep(20 * time.Millisecond)
		atomic.StoreInt32(&status, 0) // ORS lets us back in
	}()
	if _, err := o.Route(context.Background(), a, b, Walk, false); err != nil || hits != 2 {
		t.Fatalf("err %v after %d requests, want a route on the retry", err, hits)
	}
	// Too close to the deadline to wait: fall back at once.
	atomic.StoreInt32(&status, http.StatusTooManyRequests)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	orsRateBackoff = 10 * time.Second
	start := time.Now()
	if _, err := o.Route(ctx, a, geo.LatLon{Lat: 0.02}, Walk, false); !errors.Is(err, ErrRouterUnavailable) || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

// With the minute budget spent, a request waits for a slot.
func TestORSWaitsForSlot(t *testing.T) {
	var hits, status int32
	srv := fakeORS(t, &hits, &status)
	defer srv.Close()
	o := &ORS{Key: "k3y", BaseURL: srv.URL, PerMinute: 1}
	o.recent = []time.Time{time.Now().Add(-time.Minute + 50*time.Millisecond)}
	o.day = time.Now().UTC().Format("2006-01-02")
	if _, err := o.Route(context.Background(), geo.LatLon{}, geo.LatLon{Lat: 0.01}, Walk, false); err != nil || hits != 1 {
		t.Fatalf("err %v after %d requests", err, hits)
	}
}

func TestORSBudget(t *testing.T) {
	var hits, status int32
	srv := fakeORS(t, &hits, &status)
	defer srv.Close()
	o := &ORS{Key: "k3y", BaseURL: srv.URL, PerMinute: 2}
	a := geo.LatLon{Lat: 0, Lon: 0}
	// A deadline too close to wait a minute for the next slot.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var errs []error
	for i := 0; i < 3; i++ {
		_, err := o.Route(ctx, a, geo.LatLon{Lat: 0.01 * float64(i+1), Lon: 0}, Walk, false)
		errs = append(errs, err)
	}
	if errs[0] != nil || errs[1] != nil || !errors.Is(errs[2], ErrRouterUnavailable) || hits != 2 {
		t.Fatalf("errors %v after %d requests", errs, hits)
	}
	o2 := &ORS{Key: "k3y", BaseURL: srv.URL, PerDay: 1}
	o2.Route(context.Background(), a, geo.LatLon{Lat: 0.01, Lon: 0}, Walk, false)
	if _, err := o2.Route(context.Background(), a, geo.LatLon{Lat: 0.02, Lon: 0}, Walk, false); !errors.Is(err, ErrRouterUnavailable) {
		t.Fatalf("over the daily budget: %v", err)
	}
}
