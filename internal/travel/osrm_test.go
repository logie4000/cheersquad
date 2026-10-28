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

// fakeOSRM walks at 1 m/s in a straight line, except to or from island.
func fakeOSRM(t *testing.T, routes, tables *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) != 5 || parts[2] != "v1" || parts[3] != "foot" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		ps := parseCoords(t, parts[4])
		switch parts[1] {
		case "route":
			atomic.AddInt32(routes, 1)
			if r.URL.Query().Get("overview") != "false" {
				t.Errorf("route without overview=false")
			}
			if ps[0] == island || ps[1] == island {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"code":"NoRoute","message":"Impossible route between points"}`))
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"code": "Ok", "routes": []map[string]any{{"duration": geo.Distance(ps[0], ps[1])}}})
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
			json.NewEncoder(w).Encode(map[string]any{"code": "Ok", "durations": rows})
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
