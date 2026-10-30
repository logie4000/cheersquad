package travel

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/logie4000/cheersquad/internal/geo"
)

// island is a point the fake OSRM cannot route to or from.
var island = geo.LatLon{Lat: 0.5, Lon: 0.5}

// faraway is outside the fake OSRM's data: it snaps 50 km to reach a
// street, as a point in another city would.
var faraway = geo.LatLon{Lat: 0.9, Lon: 0.9}

func snaps(ps []geo.LatLon) []map[string]any {
	var ws []map[string]any
	for _, p := range ps {
		d := 0.0
		if p == faraway {
			d = 50000
		}
		ws = append(ws, map[string]any{"distance": d})
	}
	return ws
}

func parseCoords(t *testing.T, s string) []geo.LatLon {
	var ps []geo.LatLon
	for _, c := range strings.Split(s, ";") {
		ll := strings.Split(c, ",")
		lon, err1 := strconv.ParseFloat(ll[0], 64)
		lat, err2 := strconv.ParseFloat(ll[1], 64)
		if err1 != nil || err2 != nil {
			t.Errorf("bad coordinate %q", c)
		}
		ps = append(ps, geo.LatLon{Lat: lat, Lon: lon})
	}
	return ps
}

func indexes(s string, n int) []int {
	if s == "" {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	var out []int
	for _, p := range strings.Split(s, ";") {
		i, _ := strconv.Atoi(p)
		out = append(out, i)
	}
	return out
}

// fakeOSRM moves at 1 m/s in a straight line, except to or from island,
// with the foot profile.
func fakeOSRM(t *testing.T, routes, tables *int32) *httptest.Server {
	return fakeOSRMProfile(t, "foot", routes, tables)
}

// corner is the point fakeOSRM's route geometry turns at between a and b,
// as a street grid would.
func corner(a, b geo.LatLon) geo.LatLon { return geo.LatLon{Lat: a.Lat, Lon: b.Lon} }

func fakeOSRMProfile(t *testing.T, profile string, routes, tables *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) != 5 || parts[2] != "v1" || parts[3] != profile {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		ps := parseCoords(t, parts[4])
		switch parts[1] {
		case "route":
			atomic.AddInt32(routes, 1)
			q := r.URL.Query()
			full := q.Get("overview") == "full" && q.Get("geometries") == "geojson"
			if q.Get("overview") != "false" && !full {
				t.Errorf("route with overview=%q geometries=%q", q.Get("overview"), q.Get("geometries"))
			}
			if ps[0] == island || ps[1] == island {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"code":"NoRoute","message":"Impossible route between points"}`))
				return
			}
			route := map[string]any{"duration": geo.Distance(ps[0], ps[1])}
			wps := snaps(ps)
			if full {
				var line [][2]float64
				for _, p := range []geo.LatLon{ps[0], corner(ps[0], ps[1]), ps[1]} {
					line = append(line, [2]float64{p.Lon, p.Lat})
				}
				route["geometry"] = map[string]any{"type": "LineString", "coordinates": line}
			}
			json.NewEncoder(w).Encode(map[string]any{"code": "Ok", "routes": []map[string]any{route}, "waypoints": wps})
		case "table":
			atomic.AddInt32(tables, 1)
			if len(ps) > 4 {
				t.Errorf("table with %d locations exceeds 2*ChunkSize", len(ps))
			}
			src := indexes(r.URL.Query().Get("sources"), len(ps))
			dst := indexes(r.URL.Query().Get("destinations"), len(ps))
			var rows [][]*float64
			for _, i := range src {
				var row []*float64
				for _, j := range dst {
					if ps[i] == island || ps[j] == island {
						row = append(row, nil)
						continue
					}
					d := geo.Distance(ps[i], ps[j])
					row = append(row, &d)
				}
				rows = append(rows, row)
			}
			var srcs, dsts []geo.LatLon
			for _, i := range src {
				srcs = append(srcs, ps[i])
			}
			for _, j := range dst {
				dsts = append(dsts, ps[j])
			}
			json.NewEncoder(w).Encode(map[string]any{"code": "Ok", "durations": rows, "sources": snaps(srcs), "destinations": snaps(dsts)})
		default:
			t.Errorf("unexpected service %s", parts[1])
		}
	}))
}

func TestOSRMRoute(t *testing.T) {
	var routes, tables int32
	srv := fakeOSRM(t, &routes, &tables)
	defer srv.Close()
	o := &OSRM{BaseURL: srv.URL + "/"}
	a, b := geo.LatLon{Lat: 0.001, Lon: 0.002}, geo.LatLon{Lat: 0.003, Lon: -0.001}
	for i := 0; i < 2; i++ {
		d, ok := o.Duration(a, b, Walk)
		if !ok || math.Abs(d-geo.Distance(a, b)) > 0.5 {
			t.Fatalf("route: %f %v", d, ok)
		}
	}
	if routes != 1 {
		t.Fatalf("%d route requests, want 1 (cached)", routes)
	}
	if tb, rt := o.Requests(); tb != 0 || rt != 1 {
		t.Fatalf("Requests() = %d tables, %d routes", tb, rt)
	}
	if _, ok := o.Duration(a, island, Walk); ok {
		t.Fatal("NoRoute should be unreachable")
	}
	// Other modes never hit the server.
	if d, _ := o.Duration(a, b, Drive); math.Abs(d-geo.Distance(a, b)*1.3/DefaultSpeeds[Drive]) > 1e-6 || routes != 2 {
		t.Fatalf("drive: %f, %d requests", d, routes)
	}
	if o.MaxSpeed(Walk) != 2 || o.String() != "OSRM foot routing" {
		t.Fatalf("bound %f name %q", o.MaxSpeed(Walk), o.String())
	}
}

func TestOSRMStepsFollowStreets(t *testing.T) {
	var routes, tables int32
	srv := fakeOSRM(t, &routes, &tables)
	defer srv.Close()
	o := &OSRM{BaseURL: srv.URL}
	a, b := geo.LatLon{Lat: 0.001, Lon: 0.002}, geo.LatLon{Lat: 0.003, Lon: -0.001}
	steps, ok := o.Steps(a, b, Walk)
	if !ok || len(steps) != 1 {
		t.Fatalf("steps: %v %v", steps, ok)
	}
	st := steps[0]
	want := []geo.LatLon{a, a, corner(a, b), b, b}
	if st.Kind != StepWalk || len(st.Path) != len(want) {
		t.Fatalf("step %+v, want path %v", st, want)
	}
	for i := range want {
		if geo.Distance(st.Path[i], want[i]) > 0.5 {
			t.Fatalf("path[%d] = %v, want %v", i, st.Path[i], want[i])
		}
	}
	if d, _ := o.Duration(a, b, Walk); st.Seconds != d {
		t.Fatalf("step %f s, duration %f s", st.Seconds, d)
	}
	// No route: no step. Another mode: a straight step with no path.
	if _, ok := o.Steps(a, island, Walk); ok {
		t.Fatal("NoRoute should have no steps")
	}
	if steps, ok := o.Steps(a, b, Bike); !ok || steps[0].Path != nil || steps[0].Kind != StepBike {
		t.Fatalf("bike on a foot server: %+v", steps)
	}
}

func TestOSRMStepsWhenDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	o := &OSRM{BaseURL: url, Mode: Drive}
	steps, ok := o.Steps(geo.LatLon{}, geo.LatLon{Lat: 0.01}, Drive)
	if !ok || len(steps) != 1 || steps[0].Path != nil || steps[0].Kind != StepDrive {
		t.Fatalf("steps while down: %+v %v", steps, ok)
	}
}

func TestOSRMDriveFactorAndOverhead(t *testing.T) {
	var routes, tables int32
	srv := fakeOSRMProfile(t, "car", &routes, &tables)
	defer srv.Close()
	o := &OSRM{BaseURL: srv.URL, Mode: Drive, Factor: 1.5, Overhead: 600}
	a, b := geo.LatLon{}, geo.LatLon{Lat: 0.01}
	want := geo.Distance(a, b)*1.5 + 600
	if d, ok := o.Duration(a, b, Drive); !ok || math.Abs(d-want) > 0.5 {
		t.Fatalf("drive: %f %v, want %f", d, ok, want)
	}
	o.Prefetch([]geo.LatLon{b, a})
	if d, ok := o.Duration(b, a, Drive); !ok || math.Abs(d-want) > 0.5 {
		t.Fatalf("drive after prefetch: %f %v, want %f", d, ok, want)
	}
	steps, ok := o.Steps(a, b, Drive)
	if !ok || steps[0].Kind != StepDrive || len(steps[0].Path) < 2 || math.Abs(steps[0].Seconds-want) > 0.5 {
		t.Fatalf("drive steps: %+v", steps)
	}
	// Walking is the stub's on a car server.
	if d, _ := o.Duration(a, b, Walk); d != geo.Distance(a, b)*1.3/DefaultSpeeds[Walk] {
		t.Fatalf("walk on a car server: %f", d)
	}
	if o.MaxSpeed(Drive) != 40/1.5 || o.String() != "OSRM car routing" {
		t.Fatalf("bound %f name %q", o.MaxSpeed(Drive), o.String())
	}
}

func TestOSRMOutsideDataUsesFallback(t *testing.T) {
	var routes, tables int32
	srv := fakeOSRM(t, &routes, &tables)
	defer srv.Close()
	a := geo.LatLon{Lat: 0.001}
	want, _ := Haversine{}.Duration(a, faraway, Walk)
	o := &OSRM{BaseURL: srv.URL}
	if d, ok := o.Duration(a, faraway, Walk); !ok || d != want {
		t.Fatalf("route to a point outside the data: %f %v, want fallback %f", d, ok, want)
	}
	if steps, ok := o.Steps(a, faraway, Walk); !ok || steps[0].Path != nil || steps[0].Seconds != want {
		t.Fatalf("steps outside the data: %+v", steps)
	}
	p := &OSRM{BaseURL: srv.URL, ChunkSize: 2}
	p.Prefetch([]geo.LatLon{a, faraway, {}})
	before := routes
	if d, ok := p.Duration(faraway, a, Walk); !ok || d != want {
		t.Fatalf("prefetched pair outside the data: %f %v, want fallback %f", d, ok, want)
	}
	if d, ok := p.Duration(a, geo.LatLon{}, Walk); !ok || math.Abs(d-geo.Distance(a, geo.LatLon{})) > 0.5 {
		t.Fatalf("prefetched pair inside the data: %f %v", d, ok)
	}
	if routes != before {
		t.Fatal("prefetched pairs should not need route requests")
	}
}

func TestOSRMPrefetch(t *testing.T) {
	var routes, tables int32
	srv := fakeOSRM(t, &routes, &tables)
	defer srv.Close()
	o := &OSRM{BaseURL: srv.URL, ChunkSize: 2}
	ps := []geo.LatLon{{}, {Lat: 0.001}, {Lat: 0.002}, island, {Lon: 0.003}, {}}
	o.Prefetch(ps)
	if tables != 9 {
		t.Fatalf("%d table requests for 5 points in chunks of 2, want 9", tables)
	}
	for _, a := range ps {
		for _, b := range ps {
			d, ok := o.Duration(a, b, Walk)
			if a == island || b == island {
				if ok {
					t.Fatalf("%v->%v should be unreachable", a, b)
				}
				continue
			}
			if !ok || math.Abs(d-geo.Distance(a, b)) > 0.5 {
				t.Fatalf("%v->%v: %f %v", a, b, d, ok)
			}
		}
	}
	if routes != 0 {
		t.Fatalf("%d route requests after prefetch, want 0", routes)
	}
}

func TestOSRMFallsBackWhenDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	o := &OSRM{BaseURL: url}
	a, b := geo.LatLon{}, geo.LatLon{Lat: 0.01}
	d, ok := o.Duration(a, b, Walk)
	want, _ := Haversine{}.Duration(a, b, Walk)
	if !ok || d != want {
		t.Fatalf("fallback: %f %v, want %f", d, ok, want)
	}
	o.Prefetch([]geo.LatLon{a, b})
	if d, ok := o.Duration(b, a, Walk); !ok || d != want {
		t.Fatalf("fallback after prefetch: %f %v", d, ok)
	}
}

func TestOSRMMemoIsBounded(t *testing.T) {
	o := &OSRM{MaxMemo: 2}
	for i := range 5 {
		o.store([2]geo.LatLon{{Lat: float64(i)}, {}}, osrmVal{ok: true})
		if len(o.memo) > 2 {
			t.Fatalf("memo grew to %d", len(o.memo))
		}
	}
}

// ferryOSRM answers routes in 100 s, or 500 s with exclude=ferry, or refuses
// exclude like a server built without excludable ferries.
func ferryOSRM(t *testing.T, excludable bool, excludes *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d := 100.0
		if ex := r.URL.Query().Get("exclude"); ex != "" {
			atomic.AddInt32(excludes, 1)
			if ex != "ferry" || !excludable {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"code":"InvalidValue","message":"Exclude flag combination is not supported."}`))
				return
			}
			d = 500
		}
		json.NewEncoder(w).Encode(map[string]any{"code": "Ok", "routes": []map[string]any{{"duration": d}}})
	}))
}

func TestOSRMExclude(t *testing.T) {
	var excludes int32
	srv := ferryOSRM(t, true, &excludes)
	defer srv.Close()
	a, b := geo.LatLon{}, geo.LatLon{Lat: 0.01}
	o := &OSRM{BaseURL: srv.URL, Exclude: "ferry"}
	if d, ok := o.Duration(a, b, Walk); !ok || d != 500 || excludes != 1 {
		t.Fatalf("exclude=ferry: %f %v, %d requests with exclude", d, ok, excludes)
	}
	if o.String() != "OSRM foot routing without ferries" {
		t.Fatalf("name %q", o.String())
	}
	plain := &OSRM{BaseURL: srv.URL}
	if d, _ := plain.Duration(a, b, Walk); d != 100 || excludes != 1 {
		t.Fatalf("no exclude: %f, %d requests with exclude", d, excludes)
	}
}

func TestOSRMExcludeUnsupported(t *testing.T) {
	var excludes int32
	srv := ferryOSRM(t, false, &excludes)
	defer srv.Close()
	o := &OSRM{BaseURL: srv.URL, Exclude: "ferry"}
	a := geo.LatLon{}
	for i := range 3 {
		if d, ok := o.Duration(a, geo.LatLon{Lat: float64(i+1) / 100}, Walk); !ok || d != 100 {
			t.Fatalf("leg %d: %f %v, want the router's time without exclude", i, d, ok)
		}
	}
	if excludes != 1 {
		t.Fatalf("%d requests with exclude, want 1 (then routed without it)", excludes)
	}
}
