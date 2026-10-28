// Package gtfs loads a static GTFS feed into a travel.Network: the subway
// and metro lines only, with all-pairs station-to-station times precomputed.
//
// Race day and the runner's clock times are not known exactly when a feed
// is loaded, so waits use a frequency model (half the typical headway in a
// morning window on a representative Sunday) rather than exact timetables.
package gtfs

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/logie4000/cheersquad/internal/geo"
	"github.com/logie4000/cheersquad/internal/travel"
)

// Options tunes how a feed is turned into a network. A zero field means its
// default; times are seconds.
type Options struct {
	// RouteTypes are the GTFS route_type values to keep. Default
	// {1, 400, 401, 402} (subway/metro). Bus types (3, 11, 200-299,
	// 700-799, 800) are never kept, even if listed here.
	RouteTypes []int
	// WindowStart and WindowEnd bound the departures used to measure
	// headways, in seconds after the start of the service day. Default
	// 07:00 to 14:00.
	WindowStart, WindowEnd float64
	// MaxWait caps the boarding wait (half the headway). Default 900.
	MaxWait float64
	// DefaultHeadway is used when a line has fewer than two departures at
	// a station in the window. Default 600.
	DefaultHeadway float64
	// EntrySeconds is the overhead from the street to the platform at the
	// first station. Default 120.
	EntrySeconds float64
	// TransferSeconds is added when changing lines. Default 180.
	TransferSeconds float64
	// TransferWalkMeters is the farthest two distinct stations can be apart
	// and still allow a walking transfer. Default 250.
	TransferWalkMeters float64
	// ServiceDate, if set, picks the service active on that date instead of
	// a representative Sunday.
	ServiceDate time.Time
}

// DefaultRouteTypes are subway and metro route types.
var DefaultRouteTypes = []int{1, 400, 401, 402}

const (
	walkSpeed      = 1.2  // m/s for walking transfers between stations
	walkDetour     = 1.3  // street path over straight line
	fallbackSpeed  = 8.0  // m/s for a ride with no usable times in the feed
	coverPadMeters = 2000 // Covers padding around the stations
	maxSpeedMargin = 1.01
)

func (o Options) withDefaults() Options {
	if len(o.RouteTypes) == 0 {
		o.RouteTypes = DefaultRouteTypes
	}
	if o.WindowStart == 0 {
		o.WindowStart = 7 * 3600
	}
	if o.WindowEnd == 0 {
		o.WindowEnd = 14 * 3600
	}
	if o.MaxWait == 0 {
		o.MaxWait = 15 * 60
	}
	if o.DefaultHeadway == 0 {
		o.DefaultHeadway = 10 * 60
	}
	if o.EntrySeconds == 0 {
		o.EntrySeconds = 120
	}
	if o.TransferSeconds == 0 {
		o.TransferSeconds = 180
	}
	if o.TransferWalkMeters == 0 {
		o.TransferWalkMeters = 250
	}
	return o
}

func isBus(t int) bool {
	return t == 3 || t == 11 || (t >= 200 && t <= 299) || (t >= 700 && t <= 799) || t == 800
}

// Load reads the GTFS feed at path (a .zip or a directory of .txt files)
// and builds its subway network.
func Load(path string, opts Options) (*Network, error) {
	fsys, closeFeed, err := openFeed(path)
	if err != nil {
		return nil, fmt.Errorf("gtfs %s: %w", path, err)
	}
	defer closeFeed()
	name := filepath.Base(filepath.Clean(path))
	if strings.EqualFold(filepath.Ext(name), ".zip") {
		name = name[:len(name)-4]
	}
	n, err := build(fsys, name, opts.withDefaults())
	if err != nil {
		return nil, fmt.Errorf("gtfs %s: %w", path, err)
	}
	return n, nil
}

// LoadDir loads every .zip file and every subdirectory containing stops.txt
// in dir. Feeds that fail to load are skipped; their errors are joined into
// the returned error alongside the networks that did load.
func LoadDir(dir string, opts Options) ([]*Network, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var nets []*Network
	var errs []error
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(p, "stops.txt")); err != nil {
				continue
			}
		} else if !strings.EqualFold(filepath.Ext(e.Name()), ".zip") {
			continue
		}
		n, err := Load(p, opts)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		nets = append(nets, n)
	}
	return nets, errors.Join(errs...)
}

type stopRow struct {
	id, name, parent string
	pos              geo.LatLon
	hasPos           bool
}

type tripRow struct {
	route   int
	service string
	dir     string
	shape   string
}

// edgeSample is a trip that rides an edge, used to cut the edge's track
// geometry out of the trip's shape.
type edgeSample struct{ trip, k int32 }

type stopTime struct {
	seq      int
	stop     int32 // index into stops
	arr, dep int32 // -1 if missing
}

type freqRow struct {
	start, end int32
	headway    float64
}

type edgeKey struct{ route, from, to int32 }

type depKey struct {
	route, from int32
	dir         string
}

// build is the whole load once the feed's files are reachable.
func build(fsys fs.FS, fallbackName string, o Options) (*Network, error) {
	name, err := readAgency(fsys)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = fallbackName
	}

	lines, routeIdx, err := readRoutes(fsys, o.RouteTypes)
	if err != nil {
		return nil, err
	}

	trips, tripIdx, err := readTrips(fsys, routeIdx)
	if err != nil {
		return nil, err
	}

	active, err := selectService(fsys, trips, o.ServiceDate)
	if err != nil {
		return nil, err
	}
	keep := make([]bool, len(trips))
	kept := 0
	for i, t := range trips {
		keep[i] = active == nil || active[t.service]
		if keep[i] {
			kept++
		}
	}
	if kept == 0 {
		return nil, errors.New("no subway trips run on the selected service day")
	}

	stops, stopIdx, err := readStops(fsys)
	if err != nil {
		return nil, err
	}
	root := resolveStations(stops, stopIdx)

	tripStops, err := readStopTimes(fsys, tripIdx, keep, stopIdx)
	if err != nil {
		return nil, err
	}
	freqs, err := readFrequencies(fsys, tripIdx, keep)
	if err != nil {
		return nil, err
	}

	// Walk every kept trip in feed order, collecting ride observations,
	// on-train continuations and departures.
	edgeOf := map[edgeKey]int32{}
	var keys []edgeKey
	var obs [][]float64
	var edgeDirs [][]depKey
	var samples []edgeSample
	shaped := map[int32][]stopTime{} // merged stops of trips sampled for geometry
	cont := map[[2]int32]bool{}
	deps := map[depKey][]int32{}
	freqHeadways := map[depKey][]float64{}
	for ti, sts := range tripStops {
		if len(sts) < 2 {
			continue
		}
		t := trips[ti]
		merged := prepareTrip(sts, root)
		prev := int32(-1)
		for k := 0; k+1 < len(merged); k++ {
			a, b := merged[k], merged[k+1]
			ek := edgeKey{int32(t.route), a.stop, b.stop}
			e, ok := edgeOf[ek]
			if !ok {
				e = int32(len(keys))
				edgeOf[ek] = e
				keys = append(keys, ek)
				obs = append(obs, nil)
				edgeDirs = append(edgeDirs, nil)
				samples = append(samples, edgeSample{-1, -1})
			}
			if samples[e].trip < 0 && t.shape != "" {
				samples[e] = edgeSample{int32(ti), int32(k)}
				shaped[int32(ti)] = merged
			}
			if a.dep >= 0 && b.arr >= 0 && b.arr >= a.dep {
				obs[e] = append(obs[e], float64(b.arr-a.dep))
			}
			dk := depKey{int32(t.route), a.stop, "d" + t.dir}
			if t.dir == "" {
				dk.dir = "n" + strconv.Itoa(int(b.stop))
			}
			if !containsKey(edgeDirs[e], dk) {
				edgeDirs[e] = append(edgeDirs[e], dk)
			}
			if fr, ok := freqs[ti]; ok {
				for _, f := range fr {
					if float64(f.start) < o.WindowEnd && float64(f.end) > o.WindowStart {
						freqHeadways[dk] = append(freqHeadways[dk], f.headway)
					}
				}
			} else if a.dep >= 0 && float64(a.dep) >= o.WindowStart && float64(a.dep) <= o.WindowEnd {
				deps[dk] = append(deps[dk], a.dep)
			}
			if prev >= 0 {
				cont[[2]int32{prev, e}] = true
			}
			prev = e
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("subway trips have no usable stop_times")
	}

	// Stations: the served root stops, in stops.txt order.
	served := make([]bool, len(stops))
	for _, k := range keys {
		served[k.from], served[k.to] = true, true
	}
	stationOf := make([]int32, len(stops))
	var stations []travel.Station
	for i := range stops {
		stationOf[i] = -1
		if !served[i] {
			continue
		}
		if !stops[i].hasPos {
			// Fall back to any child with coordinates.
			for c := range stops {
				if root[c] == int32(i) && stops[c].hasPos {
					stops[i].pos, stops[i].hasPos = stops[c].pos, true
					break
				}
			}
		}
		if !stops[i].hasPos {
			return nil, fmt.Errorf("station %s has no coordinates", stops[i].id)
		}
		stationOf[i] = int32(len(stations))
		stations = append(stations, travel.Station{ID: stops[i].id, Name: stops[i].name, Pos: stops[i].pos})
	}

	waitOf := func(k depKey) float64 {
		h := o.DefaultHeadway
		if fh := freqHeadways[k]; len(fh) > 0 {
			h = median(fh)
		} else if gaps := headwayGaps(deps[k]); len(gaps) > 0 {
			h = median(gaps)
		}
		return math.Min(h/2, o.MaxWait)
	}

	net := &Network{
		name:     name,
		stations: stations,
		lines:    lines,
		entry:    o.EntrySeconds,
		transfer: o.TransferSeconds,
		edges:    make([]edge, len(keys)),
		next:     make([][]int32, len(keys)),
		out:      make([][]int32, len(stations)),
	}
	for e, k := range keys {
		from, to := stationOf[k.from], stationOf[k.to]
		ride := median(obs[e])
		if len(obs[e]) == 0 {
			ride = geo.Distance(stations[from].Pos, stations[to].Pos) / fallbackSpeed
		}
		wait := math.Inf(1)
		for _, dk := range edgeDirs[e] {
			wait = math.Min(wait, waitOf(dk))
		}
		net.edges[e] = edge{route: k.route, from: from, to: to, ride: ride, wait: wait}
		net.out[from] = append(net.out[from], int32(e))
	}
	contList := make([][2]int32, 0, len(cont))
	for c := range cont {
		contList = append(contList, c)
	}
	sort.Slice(contList, func(i, j int) bool {
		if contList[i][0] != contList[j][0] {
			return contList[i][0] < contList[j][0]
		}
		return contList[i][1] < contList[j][1]
	})
	for _, c := range contList {
		net.next[c[0]] = append(net.next[c[0]], c[1])
	}

	walk, err := buildTransfers(fsys, stations, stopIdx, root, stationOf, o)
	if err != nil {
		return nil, err
	}
	net.walk = walk

	if len(shaped) > 0 {
		need := map[string]bool{}
		for ti := range shaped {
			need[trips[ti].shape] = true
		}
		shapes, err := readShapes(fsys, need)
		if err != nil {
			return nil, err
		}
		net.paths = edgePaths(samples, shaped, trips, shapes, func(stop int32) geo.LatLon {
			return stations[stationOf[stop]].Pos
		})
	}

	net.solve()
	return net, nil
}

func containsKey(ks []depKey, k depKey) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

// headwayGaps returns the gaps between distinct sorted departure times.
func headwayGaps(deps []int32) []float64 {
	if len(deps) < 2 {
		return nil
	}
	d := append([]int32(nil), deps...)
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	var gaps []float64
	for i := 1; i < len(d); i++ {
		if d[i] != d[i-1] {
			gaps = append(gaps, float64(d[i]-d[i-1]))
		}
	}
	return gaps
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

// prepareTrip sorts a trip's stop times, fills missing times, maps stops to
// their stations and merges consecutive stops in the same station.
func prepareTrip(sts []stopTime, root []int32) []stopTime {
	sort.SliceStable(sts, func(i, j int) bool { return sts[i].seq < sts[j].seq })
	for i := range sts {
		if sts[i].arr < 0 {
			sts[i].arr = sts[i].dep
		}
		if sts[i].dep < 0 {
			sts[i].dep = sts[i].arr
		}
	}
	// Interpolate stops with no times evenly between timepoints.
	last := -1
	for i := range sts {
		if sts[i].dep < 0 {
			continue
		}
		if last >= 0 && i-last > 1 {
			t0, t1 := float64(sts[last].dep), float64(sts[i].arr)
			for k := last + 1; k < i; k++ {
				v := int32(math.Round(t0 + (t1-t0)*float64(k-last)/float64(i-last)))
				sts[k].arr, sts[k].dep = v, v
			}
		}
		last = i
	}
	out := make([]stopTime, 0, len(sts))
	for _, s := range sts {
		s.stop = root[s.stop]
		if n := len(out); n > 0 && out[n-1].stop == s.stop {
			if s.dep >= 0 {
				out[n-1].dep = s.dep
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

func readAgency(fsys fs.FS) (string, error) {
	t, err := openTable(fsys, "agency.txt", false)
	if err != nil {
		return "", err
	}
	defer t.close()
	for {
		ok, err := t.next()
		if err != nil || !ok {
			return "", err
		}
		if n := t.get("agency_name"); n != "" {
			return n, nil
		}
	}
}

func readRoutes(fsys fs.FS, types []int) ([]line, map[string]int, error) {
	t, err := openTable(fsys, "routes.txt", true)
	if err != nil {
		return nil, nil, err
	}
	defer t.close()
	if err := t.requireCols("route_id", "route_type"); err != nil {
		return nil, nil, err
	}
	allowed := map[int]bool{}
	for _, rt := range types {
		if !isBus(rt) {
			allowed[rt] = true
		}
	}
	var lines []line
	idx := map[string]int{}
	for {
		ok, err := t.next()
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			break
		}
		rt, err := strconv.Atoi(t.get("route_type"))
		if err != nil || !allowed[rt] {
			continue
		}
		id := t.get("route_id")
		label := t.get("route_short_name")
		if label == "" {
			label = t.get("route_long_name")
		}
		if label == "" {
			label = id
		}
		idx[id] = len(lines)
		lines = append(lines, line{label: label, color: strings.TrimPrefix(t.get("route_color"), "#")})
	}
	if len(lines) == 0 {
		return nil, nil, fmt.Errorf("no subway or metro routes (route_type in %v) in routes.txt", types)
	}
	return lines, idx, nil
}

func readTrips(fsys fs.FS, routeIdx map[string]int) ([]tripRow, map[string]int, error) {
	t, err := openTable(fsys, "trips.txt", true)
	if err != nil {
		return nil, nil, err
	}
	defer t.close()
	if err := t.requireCols("trip_id", "route_id"); err != nil {
		return nil, nil, err
	}
	var trips []tripRow
	idx := map[string]int{}
	for {
		ok, err := t.next()
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			break
		}
		r, ok := routeIdx[t.get("route_id")]
		if !ok {
			continue
		}
		idx[t.get("trip_id")] = len(trips)
		trips = append(trips, tripRow{route: r, service: t.get("service_id"), dir: t.get("direction_id"), shape: t.get("shape_id")})
	}
	if len(trips) == 0 {
		return nil, nil, errors.New("no trips for subway routes in trips.txt")
	}
	return trips, idx, nil
}

// selectService returns the service_ids to use, or nil for all trips. With
// no date given it picks the Sunday on which the most subway trips run;
// if no Sunday has service per calendar.txt/calendar_dates.txt it returns
// nil.
func selectService(fsys fs.FS, trips []tripRow, date time.Time) (map[string]bool, error) {
	type calRow struct {
		days       [7]bool // by time.Weekday
		start, end time.Time
	}
	cal := map[string]calRow{}
	t, err := openTable(fsys, "calendar.txt", false)
	if err != nil {
		return nil, err
	}
	dayCols := [7]string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}
	for {
		ok, err := t.next()
		if err != nil {
			t.close()
			return nil, err
		}
		if !ok {
			break
		}
		var c calRow
		for d, col := range dayCols {
			c.days[d] = t.get(col) == "1"
		}
		var ok1, ok2 bool
		c.start, ok1 = parseDate(t.get("start_date"))
		c.end, ok2 = parseDate(t.get("end_date"))
		if !ok1 || !ok2 {
			continue
		}
		cal[t.get("service_id")] = c
	}
	t.close()

	exc := map[time.Time]map[string]bool{} // date -> service -> added
	t, err = openTable(fsys, "calendar_dates.txt", false)
	if err != nil {
		return nil, err
	}
	for {
		ok, err := t.next()
		if err != nil {
			t.close()
			return nil, err
		}
		if !ok {
			break
		}
		d, ok := parseDate(t.get("date"))
		if !ok {
			continue
		}
		et := t.get("exception_type")
		if et != "1" && et != "2" {
			continue
		}
		if exc[d] == nil {
			exc[d] = map[string]bool{}
		}
		exc[d][t.get("service_id")] = et == "1"
	}
	t.close()

	perService := map[string]int{}
	for _, tr := range trips {
		perService[tr.service]++
	}
	activeOn := func(d time.Time) (map[string]bool, int) {
		set := map[string]bool{}
		n := 0
		for svc, cnt := range perService {
			on := false
			if added, ok := exc[d][svc]; ok {
				on = added
			} else if c, ok := cal[svc]; ok {
				on = c.days[d.Weekday()] && !d.Before(c.start) && !d.After(c.end)
			}
			if on {
				set[svc] = true
				n += cnt
			}
		}
		return set, n
	}

	if !date.IsZero() {
		d := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
		set, n := activeOn(d)
		if n == 0 {
			return nil, fmt.Errorf("no subway service on %s", d.Format("2006-01-02"))
		}
		return set, nil
	}

	var candidates []time.Time
	for _, c := range cal {
		if !c.days[time.Sunday] {
			continue
		}
		d := c.start.AddDate(0, 0, (7-int(c.start.Weekday()))%7)
		for k := 0; !d.After(c.end) && k < 1000; k++ {
			candidates = append(candidates, d)
			d = d.AddDate(0, 0, 7)
		}
	}
	for d := range exc {
		if d.Weekday() == time.Sunday {
			candidates = append(candidates, d)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Before(candidates[j]) })
	var best map[string]bool
	bestN := 0
	for i, d := range candidates {
		if i > 0 && d.Equal(candidates[i-1]) {
			continue
		}
		if set, n := activeOn(d); n > bestN {
			best, bestN = set, n
		}
	}
	return best, nil
}

func readStops(fsys fs.FS) ([]stopRow, map[string]int32, error) {
	t, err := openTable(fsys, "stops.txt", true)
	if err != nil {
		return nil, nil, err
	}
	defer t.close()
	if err := t.requireCols("stop_id"); err != nil {
		return nil, nil, err
	}
	var stops []stopRow
	idx := map[string]int32{}
	for {
		ok, err := t.next()
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			break
		}
		s := stopRow{id: t.get("stop_id"), name: t.get("stop_name"), parent: t.get("parent_station")}
		lat, err1 := strconv.ParseFloat(t.get("stop_lat"), 64)
		lon, err2 := strconv.ParseFloat(t.get("stop_lon"), 64)
		if err1 == nil && err2 == nil && (lat != 0 || lon != 0) {
			s.pos, s.hasPos = geo.LatLon{Lat: lat, Lon: lon}, true
		}
		if _, dup := idx[s.id]; dup {
			continue
		}
		idx[s.id] = int32(len(stops))
		stops = append(stops, s)
	}
	return stops, idx, nil
}

// resolveStations maps each stop to the stop that stands for its station:
// its topmost parent (a location_type 1 station; boarding areas go via
// their platform), or itself if it has none.
func resolveStations(stops []stopRow, idx map[string]int32) []int32 {
	root := make([]int32, len(stops))
	for i := range stops {
		cur := int32(i)
		for depth := 0; depth < 4; depth++ {
			p, ok := idx[stops[cur].parent]
			if stops[cur].parent == "" || !ok || p == cur {
				break
			}
			cur = p
		}
		root[i] = cur
	}
	return root
}

func readStopTimes(fsys fs.FS, tripIdx map[string]int, keep []bool, stopIdx map[string]int32) ([][]stopTime, error) {
	t, err := openTable(fsys, "stop_times.txt", true)
	if err != nil {
		return nil, err
	}
	defer t.close()
	if err := t.requireCols("trip_id", "stop_id", "stop_sequence"); err != nil {
		return nil, err
	}
	out := make([][]stopTime, len(keep))
	for {
		ok, err := t.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		ti, ok := tripIdx[t.get("trip_id")]
		if !ok || !keep[ti] {
			continue
		}
		si, ok := stopIdx[t.get("stop_id")]
		if !ok {
			continue
		}
		seq, err := strconv.Atoi(t.get("stop_sequence"))
		if err != nil {
			continue
		}
		st := stopTime{seq: seq, stop: si, arr: -1, dep: -1}
		if v, ok := parseTime(t.get("arrival_time")); ok {
			st.arr = v
		}
		if v, ok := parseTime(t.get("departure_time")); ok {
			st.dep = v
		}
		out[ti] = append(out[ti], st)
	}
	return out, nil
}

func readFrequencies(fsys fs.FS, tripIdx map[string]int, keep []bool) (map[int][]freqRow, error) {
	t, err := openTable(fsys, "frequencies.txt", false)
	if err != nil {
		return nil, err
	}
	defer t.close()
	out := map[int][]freqRow{}
	for {
		ok, err := t.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		ti, ok := tripIdx[t.get("trip_id")]
		if !ok || !keep[ti] {
			continue
		}
		start, ok1 := parseTime(t.get("start_time"))
		end, ok2 := parseTime(t.get("end_time"))
		h, err := strconv.ParseFloat(t.get("headway_secs"), 64)
		if !ok1 || !ok2 || err != nil || h <= 0 {
			continue
		}
		out[ti] = append(out[ti], freqRow{start: start, end: end, headway: h})
	}
	return out, nil
}

// buildTransfers returns, per station, the other stations a rider can
// transfer to on foot and what that costs: transfers.txt min_transfer_time
// when given, otherwise TransferSeconds plus the walk for stations within
// TransferWalkMeters (or any distance when transfers.txt lists the pair).
// transfer_type 3 forbids a pair.
func buildTransfers(fsys fs.FS, stations []travel.Station, stopIdx map[string]int32, root, stationOf []int32, o Options) ([][]walkLink, error) {
	type pair struct{ a, b int32 }
	walkCost := func(a, b int32) float64 {
		d := geo.Distance(stations[a].Pos, stations[b].Pos)
		return o.TransferSeconds + d*walkDetour/walkSpeed
	}
	cost := map[pair]float64{}
	n := len(stations)
	for a := 0; a < n; a++ {
		for b := a + 1; b < n; b++ {
			if geo.Distance(stations[a].Pos, stations[b].Pos) <= o.TransferWalkMeters {
				c := walkCost(int32(a), int32(b))
				cost[pair{int32(a), int32(b)}] = c
				cost[pair{int32(b), int32(a)}] = c
			}
		}
	}

	t, err := openTable(fsys, "transfers.txt", false)
	if err != nil {
		return nil, err
	}
	defer t.close()
	explicit := map[pair]float64{}
	forbidden := map[pair]bool{}
	for {
		ok, err := t.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		fi, ok1 := stopIdx[t.get("from_stop_id")]
		ti, ok2 := stopIdx[t.get("to_stop_id")]
		if !ok1 || !ok2 {
			continue
		}
		a, b := stationOf[root[fi]], stationOf[root[ti]]
		if a < 0 || b < 0 || a == b {
			continue
		}
		p := pair{a, b}
		if t.get("transfer_type") == "3" {
			forbidden[p] = true
			continue
		}
		c := walkCost(a, b)
		if v, err := strconv.ParseFloat(t.get("min_transfer_time"), 64); err == nil && v >= 0 {
			c = v
		}
		if old, ok := explicit[p]; !ok || c < old {
			explicit[p] = c
		}
	}
	for p, c := range explicit {
		cost[p] = c
	}
	for p := range forbidden {
		delete(cost, p)
	}

	walk := make([][]walkLink, n)
	for p, c := range cost {
		walk[p.a] = append(walk[p.a], walkLink{to: p.b, cost: c})
	}
	for _, w := range walk {
		sort.Slice(w, func(i, j int) bool { return w[i].to < w[j].to })
	}
	return walk, nil
}
