package travel

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logie4000/cheersquad/internal/geo"
)

// A restarted server answers from the cache file without asking ORS.
func TestORSCacheOutlivesRestart(t *testing.T) {
	var dirHits, matHits, status int32
	dirSrv := fakeORS(t, &dirHits, &status)
	defer dirSrv.Close()
	matSrv := fakeMatrix(t, &matHits)
	defer matSrv.Close()
	path := filepath.Join(t.TempDir(), "sub", "ors.jsonl")
	ctx := context.Background()
	a, b, c := geo.LatLon{Lat: 40.1, Lon: -73.9}, geo.LatLon{Lat: 40.11, Lon: -73.9}, geo.LatLon{Lat: 40.12, Lon: -73.91}
	pairs := []Pair{{a, b}, {a, c}, {b, island}}

	o := &ORS{Key: "k3y", BaseURL: dirSrv.URL}
	if err := o.OpenCache(path); err != nil {
		t.Fatal(err)
	}
	want, err := o.Route(ctx, a, b, Walk, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Route(ctx, a, island, Walk, false); err != ErrNoStreetRoute {
		t.Fatalf("island: %v", err)
	}
	o.BaseURL = matSrv.URL
	wantTimes, err := o.Times(ctx, pairs, Walk)
	if err != nil {
		t.Fatal(err)
	}
	o.Close()

	o2 := &ORS{Key: "k3y", BaseURL: "http://127.0.0.1:1"} // any request fails
	if err := o2.OpenCache(path); err != nil {
		t.Fatal(err)
	}
	defer o2.Close()
	got, err := o2.Route(ctx, a, b, Walk, true)
	if err != nil || got.Seconds != want.Seconds || got.Meters != want.Meters || len(got.Path) != len(want.Path) || got.Path[1] != want.Path[1] {
		t.Fatalf("cached route %+v, %v; want %+v", got, err, want)
	}
	if _, err := o2.Route(ctx, a, island, Walk, false); err != ErrNoStreetRoute {
		t.Fatalf("cached no-route: %v", err)
	}
	if _, err := o2.Route(ctx, a, b, Walk, false); err != ErrRouterUnavailable {
		t.Fatalf("with ferries is a different leg, not cached: %v", err)
	}
	gotTimes, err := o2.Times(ctx, pairs, Walk)
	if err != nil || gotTimes[Pair{a, c}] != wantTimes[Pair{a, c}] || !math.IsNaN(gotTimes[Pair{b, island}]) {
		t.Fatalf("cached times %v, %v; want %v", gotTimes, err, wantTimes)
	}
	if _, err := o2.Times(ctx, pairs, Bike); err == nil {
		t.Fatal("bike times were never asked for, so should not be cached")
	}
	if dirHits != 2 || matHits != 1 {
		t.Fatalf("%d directions and %d matrix requests; want 2 and 1", dirHits, matHits)
	}
}

// Old answers, repeats and a torn last line are dropped on load, and the
// file is compacted.
func TestORSCacheDropsOldAndCompacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ors.jsonl")
	a, b := geo.LatLon{Lat: 1, Lon: 2}, geo.LatLon{Lat: 1.01, Lon: 2}
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	for i := 0; i < 300; i++ { // the same leg, answered many times
		enc.Encode(cacheLine{Kind: "m", Mode: Walk, A: pair(a), B: pair(b), Seconds: 100, Day: today()})
	}
	enc.Encode(cacheLine{Kind: "m", Mode: Walk, A: pair(b), B: pair(a), Seconds: 100, Day: today() - 365})
	sb.WriteString(`{"k":"m","m":"walk","a":[1,`) // torn by a crash
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	o := &ORS{Key: "k3y", BaseURL: "http://127.0.0.1:1"}
	if err := o.OpenCache(path); err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if len(o.times) != 1 {
		t.Fatalf("kept %d times, want 1", len(o.times))
	}
	raw, _ := os.ReadFile(path)
	if n := strings.Count(string(raw), "\n"); n != 1 {
		t.Fatalf("compacted file has %d lines, want 1", n)
	}
}

// Over the cap, the oldest answers go first.
func TestORSTrimKeepsNewest(t *testing.T) {
	o := &ORS{MaxTimes: 10}
	o.times = map[orsKey]orsTime{}
	for i := 0; i < 11; i++ {
		o.times[orsKey{a: geo.LatLon{Lat: float64(i)}, m: Walk}] = orsTime{1, int64(i)}
	}
	o.trimLocked()
	if len(o.times) != 9 {
		t.Fatalf("%d left, want 9", len(o.times))
	}
	for k, v := range o.times {
		if v.day < 2 {
			t.Fatalf("kept old %v", k)
		}
	}
}
