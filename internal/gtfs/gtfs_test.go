package gtfs

import (
	"archive/zip"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/logie4000/cheersquad/internal/geo"
	"github.com/logie4000/cheersquad/internal/travel"
)

const fixture = "testdata/tiny"

// The fixture (testdata/tiny), with default options:
//
//	Red (R, route_type 1):  A --270s-- X --300s-- B   eastbound every 10 min
//	                        A --240s-- X --300s-- B   westbound every 10 min
//	  A->X eastbound is the median of 240, 240, 300 and a 24:50 trip
//	  taking 360 (so 270); two weekday-only trips every 5 min must not count.
//	Green (no short name):  C --180-- X --180-- D southbound every 40 min
//	  (wait capped at 15 min); one northbound trip (default 10 min headway).
//	M (route_type 401, frequencies.txt every 6 min): W --420s--> F.
//	W is 85 m from B; transfers.txt gives B->W 200 s.
//	Buses B1 (A->D in 60 s) and B2 (via stop Z) must be ignored.
const (
	entry    = 120.0
	transfer = 180.0
	waitR    = 300.0 // 600 s headway / 2
	waitG    = 900.0 // 2400 s headway / 2, capped
	waitGN   = 300.0 // default 600 s headway / 2
	waitM    = 180.0 // 360 s frequency / 2
)

func load(t *testing.T, path string, o Options) *Network {
	t.Helper()
	n, err := Load(path, o)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func indexes(t *testing.T, n travel.Network) map[string]int {
	t.Helper()
	idx := map[string]int{}
	for i, s := range n.Stations() {
		idx[s.ID] = i
	}
	return idx
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestStationsAndName(t *testing.T) {
	n := load(t, fixture, Options{})
	if n.Name() != "Test Transit" {
		t.Errorf("Name = %q", n.Name())
	}
	var ids []string
	for _, s := range n.Stations() {
		ids = append(ids, s.ID)
	}
	if got := strings.Join(ids, ","); got != "A,X,B,W,F,C,D" {
		t.Errorf("stations = %s, want A,X,B,W,F,C,D (parents, no platforms, no bus-only Z)", got)
	}
	x := n.Stations()[indexes(t, n)["X"]]
	if x.Name != "Cross, Street" || x.Pos != (geo.LatLon{Lat: 40, Lon: -74}) {
		t.Errorf("X = %+v", x)
	}
}

func TestHandCheckedTimes(t *testing.T) {
	n := load(t, fixture, Options{})
	idx := indexes(t, n)
	cases := []struct {
		from, to string
		want     float64
		rides    []travel.Ride
	}{
		{"A", "B", entry + waitR + 270 + 300, []travel.Ride{{Line: "R", Color: "EE352E", Seconds: waitR + 570}}},
		{"B", "A", entry + waitR + 300 + 240, []travel.Ride{{Line: "R", Seconds: waitR + 540}}},
		{"X", "B", entry + waitR + 300, []travel.Ride{{Line: "R", Seconds: waitR + 300}}},
		// Change from Red to Green at X; the bus straight to D is ignored.
		{"A", "D", entry + waitR + 270 + transfer + waitG + 180, []travel.Ride{
			{Line: "R", Seconds: waitR + 270}, {Line: "Green Line", Color: "00933C", Seconds: waitG + 180}}},
		{"C", "A", entry + waitG + 180 + transfer + waitR + 240, []travel.Ride{
			{Line: "Green Line", Seconds: waitG + 180}, {Line: "R", Seconds: waitR + 240}}},
		// Stays on the train through X; X->D uses an interpolated time.
		{"C", "D", entry + waitG + 360, []travel.Ride{{Line: "Green Line", Seconds: waitG + 360}}},
		{"D", "C", entry + waitGN + 360, []travel.Ride{{Line: "Green Line", Seconds: waitGN + 360}}},
		// Walk B->W using transfers.txt's 200 s, then the frequency-based M.
		{"A", "F", entry + waitR + 570 + 200 + waitM + 420, []travel.Ride{
			{Line: "R", Seconds: waitR + 570}, {Line: "M", Seconds: waitM + 420}}},
		// Trips start by boarding at the entry station, so B->F rides to X
		// and back to reach the B->W transfer. Walking to W first is the
		// caller's job. Re-boarding R is a new segment with its own wait but
		// no line-change penalty.
		{"B", "F", entry + waitR + 300 + waitR + 300 + 200 + waitM + 420, []travel.Ride{
			{Line: "R", Seconds: waitR + 300}, {Line: "R", Seconds: waitR + 300}, {Line: "M", Seconds: waitM + 420}}},
	}
	for _, c := range cases {
		i, j := idx[c.from], idx[c.to]
		got, ok := n.Time(i, j)
		if !ok || !near(got, c.want) {
			t.Errorf("Time(%s,%s) = %v, %v; want %v", c.from, c.to, got, ok, c.want)
		}
		rides := n.Rides(i, j)
		if len(rides) != len(c.rides) {
			t.Errorf("Rides(%s,%s) = %+v, want %+v", c.from, c.to, rides, c.rides)
			continue
		}
		for k, r := range rides {
			w := c.rides[k]
			if r.Line != w.Line || !near(r.Seconds, w.Seconds) || (w.Color != "" && r.Color != w.Color) {
				t.Errorf("Rides(%s,%s)[%d] = %+v, want %+v", c.from, c.to, k, r, w)
			}
		}
		if rides[0].From != i || rides[len(rides)-1].To != j {
			t.Errorf("Rides(%s,%s) endpoints = %d..%d", c.from, c.to, rides[0].From, rides[len(rides)-1].To)
		}
	}
	if r := n.Rides(idx["A"], idx["F"]); r[0].To != idx["B"] || r[1].From != idx["W"] {
		t.Errorf("A->F should ride to B and board at W: %+v", r)
	}
}

func TestSelfAndUnreachable(t *testing.T) {
	n := load(t, fixture, Options{})
	idx := indexes(t, n)
	for i := range n.Stations() {
		if got, ok := n.Time(i, i); !ok || got != 0 || n.Rides(i, i) != nil {
			t.Errorf("Time(%d,%d) = %v, %v", i, i, got, ok)
		}
	}
	// M only runs W->F, and F has nothing else.
	for _, c := range [][2]string{{"F", "A"}, {"F", "W"}, {"W", "B"}, {"W", "A"}} {
		if got, ok := n.Time(idx[c[0]], idx[c[1]]); ok {
			t.Errorf("Time(%s,%s) = %v, want unreachable", c[0], c[1], got)
		}
		if r := n.Rides(idx[c[0]], idx[c[1]]); r != nil {
			t.Errorf("Rides(%s,%s) = %+v, want nil", c[0], c[1], r)
		}
	}
	if _, ok := n.Time(-1, 0); ok {
		t.Error("out of range index should be unreachable")
	}
}

// Rides plus entry plus transfers must add up to Time for every pair.
func TestRidesSumToTime(t *testing.T) {
	n := load(t, fixture, Options{})
	idx := indexes(t, n)
	st := n.Stations()
	transferCost := func(a, b travel.Ride) float64 {
		switch {
		case a.To == b.From && a.Line == b.Line:
			return 0
		case a.To == b.From:
			return transfer
		case a.To == idx["B"] && b.From == idx["W"]:
			return 200
		default:
			return transfer + geo.Distance(st[a.To].Pos, st[b.From].Pos)*1.3/1.2
		}
	}
	pairs := 0
	for i := range st {
		for j := range st {
			got, ok := n.Time(i, j)
			if !ok || i == j {
				continue
			}
			pairs++
			rides := n.Rides(i, j)
			if len(rides) == 0 || rides[0].From != i || rides[len(rides)-1].To != j {
				t.Fatalf("Rides(%d,%d) = %+v", i, j, rides)
			}
			sum := entry
			for k, r := range rides {
				sum += r.Seconds
				if k > 0 {
					sum += transferCost(rides[k-1], r)
				}
			}
			if !near(sum, got) {
				t.Errorf("%s->%s: rides sum to %v, Time = %v (%+v)", st[i].ID, st[j].ID, sum, got, rides)
			}
		}
	}
	if pairs < 20 {
		t.Errorf("only %d reachable pairs", pairs)
	}
}

func TestMaxSpeedIsBound(t *testing.T) {
	n := load(t, fixture, Options{})
	st := n.Stations()
	fastest := 0.0
	for i := range st {
		for j := range st {
			got, ok := n.Time(i, j)
			if !ok || i == j {
				continue
			}
			v := geo.Distance(st[i].Pos, st[j].Pos) / got
			if v > n.MaxSpeed() {
				t.Errorf("%s->%s at %v m/s beats MaxSpeed %v", st[i].ID, st[j].ID, v, n.MaxSpeed())
			}
			fastest = math.Max(fastest, v)
		}
	}
	if fastest == 0 || math.Abs(n.MaxSpeed()-fastest*1.01) > 1e-9 {
		t.Errorf("MaxSpeed = %v, want %v", n.MaxSpeed(), fastest*1.01)
	}
}

func TestCovers(t *testing.T) {
	n := load(t, fixture, Options{})
	for _, c := range []struct {
		p    geo.LatLon
		want bool
	}{
		{geo.LatLon{Lat: 40, Lon: -74}, true},
		{geo.LatLon{Lat: 40.03, Lon: -74.03}, true},   // ~1 km past C and A
		{geo.LatLon{Lat: 40.06, Lon: -74}, false},     // ~4.4 km north of C
		{geo.LatLon{Lat: 40, Lon: -73.92}, false},     // ~3.4 km east of F
		{geo.LatLon{Lat: 39.965, Lon: -73.945}, true}, // ~1.7 km past D/F
	} {
		if got := n.Covers(c.p); got != c.want {
			t.Errorf("Covers(%v) = %v", c.p, got)
		}
	}
}

func TestBusesNeverIncluded(t *testing.T) {
	n := load(t, fixture, Options{RouteTypes: []int{1, 3, 401, 700}})
	idx := indexes(t, n)
	if _, ok := idx["Z"]; ok {
		t.Error("bus-only stop Z is a station")
	}
	if got, _ := n.Time(idx["A"], idx["D"]); !near(got, entry+waitR+270+transfer+waitG+180) {
		t.Errorf("Time(A,D) = %v; the bus must not be used", got)
	}
	if _, err := Load(fixture, Options{RouteTypes: []int{2, 3}}); err == nil ||
		!strings.Contains(err.Error(), "no subway") {
		t.Errorf("rail+bus only: err = %v, want no subway routes", err)
	}
}

// On a Monday only the weekday Red trips run: every 5 min, 240 s to X.
func TestServiceDate(t *testing.T) {
	n := load(t, fixture, Options{ServiceDate: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)})
	idx := indexes(t, n)
	if len(n.Stations()) != 3 {
		t.Errorf("stations = %+v, want A, X, B", n.Stations())
	}
	if got, _ := n.Time(idx["A"], idx["B"]); !near(got, entry+150+240+300) {
		t.Errorf("Monday Time(A,B) = %v", got)
	}
	if _, err := Load(fixture, Options{ServiceDate: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)}); err == nil {
		t.Error("date outside the calendar should fail")
	}
}

func TestOptionsChangeTimes(t *testing.T) {
	n := load(t, fixture, Options{EntrySeconds: 60, TransferSeconds: 30, MaxWait: 100})
	idx := indexes(t, n)
	if got, _ := n.Time(idx["A"], idx["D"]); !near(got, 60+100+270+30+100+180) {
		t.Errorf("Time(A,D) = %v", got)
	}
}

func zipDir(t *testing.T, dir, dst, prefix string) {
	t.Helper()
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		w, err := zw.Create(prefix + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		src, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(w, src); err != nil {
			t.Fatal(err)
		}
		src.Close()
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestZipMatchesDirectory(t *testing.T) {
	tmp := t.TempDir()
	flat := filepath.Join(tmp, "flat.zip")
	nested := filepath.Join(tmp, "nested.zip")
	zipDir(t, fixture, flat, "")
	zipDir(t, fixture, nested, "google_transit/")
	want := load(t, fixture, Options{})
	for _, p := range []string{flat, nested} {
		got := load(t, p, Options{})
		if len(got.Stations()) != len(want.Stations()) {
			t.Fatalf("%s: %d stations", p, len(got.Stations()))
		}
		for i := range want.Stations() {
			for j := range want.Stations() {
				a, aok := want.Time(i, j)
				b, bok := got.Time(i, j)
				if a != b || aok != bok {
					t.Errorf("%s: Time(%d,%d) = %v, want %v", p, i, j, b, a)
				}
			}
		}
	}
}

func TestLoadDir(t *testing.T) {
	tmp := t.TempDir()
	zipDir(t, fixture, filepath.Join(tmp, "one.zip"), "")
	sub := filepath.Join(tmp, "two")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(fixture)
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(fixture, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if e.Name() == "agency.txt" {
			continue // falls back to the directory name
		}
		if err := os.WriteFile(filepath.Join(sub, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "broken.zip"), []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(tmp, "notes.txt"), []byte("ignored"), 0o644)
	os.Mkdir(filepath.Join(tmp, "empty"), 0o755)

	nets, err := LoadDir(tmp, Options{})
	if len(nets) != 2 {
		t.Fatalf("loaded %d networks, want 2", len(nets))
	}
	if nets[0].Name() != "Test Transit" || nets[1].Name() != "two" {
		t.Errorf("names = %q, %q", nets[0].Name(), nets[1].Name())
	}
	if err == nil || !strings.Contains(err.Error(), "broken.zip") {
		t.Errorf("err = %v, want broken.zip reported", err)
	}
}

func TestParseTime(t *testing.T) {
	for s, want := range map[string]int32{"08:00:00": 28800, "7:05:09": 25509, "25:02:00": 90120, " 24:00:00": 86400} {
		if got, ok := parseTime(s); !ok || got != want {
			t.Errorf("parseTime(%q) = %v, %v", s, got, ok)
		}
	}
	for _, s := range []string{"", "8:00", "08:61:00", "aa:00:00"} {
		if _, ok := parseTime(s); ok {
			t.Errorf("parseTime(%q) should fail", s)
		}
	}
}
