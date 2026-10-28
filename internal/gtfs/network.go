package gtfs

import (
	"math"

	"github.com/logie4000/cheersquad/internal/geo"
	"github.com/logie4000/cheersquad/internal/travel"
)

// line is one kept GTFS route.
type line struct {
	label string
	color string
	rail  bool // kept only if frequent; see Options.RailTypes
}

// edge is a ride on one route between consecutive stations of its trips.
type edge struct {
	route    int32
	from, to int32   // station indexes
	ride     float64 // median seconds between the two stations
	wait     float64 // boarding wait at from for this route and direction
}

type walkLink struct {
	to   int32
	cost float64
}

// Network is a GTFS feed's subway network with all-pairs times
// precomputed. It implements travel.Network and is safe for concurrent
// reads.
type Network struct {
	name     string
	stations []travel.Station
	lines    []line
	edges    []edge
	next     [][]int32 // per edge: edges a train continues onto
	out      [][]int32 // per station: edges leaving it
	walk     [][]walkLink
	// paths[e] is edge e's track geometry from shapes.txt, or nil. paths
	// itself is nil when the feed has no shapes.
	paths    [][]geo.LatLon
	entry    float64
	transfer float64

	// times[i*n+j] is Time(i, j), or -1 if unreachable.
	times []float32
	// last[i*n+j] is the edge state the fastest i->j trip ends on.
	last []int32
	// pred[i][e] is the state before e on the fastest trip from i, shifted
	// left one bit, with the low bit set if the rider boarded at e. -1
	// means e was boarded at i itself.
	pred [][]int32

	maxSpeed float64
	minLat   float64
	maxLat   float64
	minLon   float64
	maxLon   float64
}

var _ travel.Network = (*Network)(nil)

func (n *Network) Name() string { return n.name }

// Stations returns the network's stations; indexes into it are what Time
// and Rides take. The slice must not be modified.
func (n *Network) Stations() []travel.Station { return n.stations }

func (n *Network) valid(i, j int) bool {
	return i >= 0 && j >= 0 && i < len(n.stations) && j < len(n.stations)
}

func (n *Network) Time(i, j int) (float64, bool) {
	if !n.valid(i, j) {
		return 0, false
	}
	t := n.times[i*len(n.stations)+j]
	if t < 0 {
		return 0, false
	}
	return float64(t), true
}

func (n *Network) Rides(i, j int) []travel.Ride {
	if !n.valid(i, j) || i == j {
		return nil
	}
	st := n.last[i*len(n.stations)+j]
	if st < 0 {
		return nil
	}
	// Walk back to the source, then emit segments front to back.
	var states []int32
	var boarded []bool
	pred := n.pred[i]
	for st >= 0 {
		p := pred[st]
		states = append(states, st)
		boarded = append(boarded, p&1 == 1)
		st = p >> 1
	}
	var rides []travel.Ride
	for k := len(states) - 1; k >= 0; k-- {
		e := n.edges[states[k]]
		if boarded[k] {
			l := n.lines[e.route]
			rides = append(rides, travel.Ride{
				Line: l.label, Color: l.color,
				From: int(e.from), To: int(e.to),
				Seconds: e.wait + e.ride,
			})
			continue
		}
		r := &rides[len(rides)-1]
		r.Via = append(r.Via, r.To)
		r.To = int(e.to)
		r.Seconds += e.ride
	}
	if n.paths != nil {
		r := -1
		for k := len(states) - 1; k >= 0; k-- {
			if boarded[k] {
				r++
			}
			rides[r].Path = n.appendPath(rides[r].Path, states[k])
		}
	}
	return rides
}

// appendPath extends a ride's track with edge e's geometry, or a straight
// line between its stations when the feed has no shape for it.
func (n *Network) appendPath(path []geo.LatLon, e int32) []geo.LatLon {
	seg := n.paths[e]
	if len(seg) < 2 {
		ed := n.edges[e]
		seg = []geo.LatLon{n.stations[ed.from].Pos, n.stations[ed.to].Pos}
	}
	if len(path) > 0 && path[len(path)-1] == seg[0] {
		seg = seg[1:]
	}
	return append(path, seg...)
}

func (n *Network) MaxSpeed() float64 { return n.maxSpeed }

func (n *Network) Covers(p geo.LatLon) bool {
	return p.Lat >= n.minLat && p.Lat <= n.maxLat && p.Lon >= n.minLon && p.Lon <= n.maxLon
}

// solve runs Dijkstra from every station over edge states (being on a
// route's train having just ridden one edge), so that staying on a train,
// changing lines in a station and walking to a nearby station each cost
// what they should. It then fills times, MaxSpeed and the bounding box.
func (n *Network) solve() {
	ns, ne := len(n.stations), len(n.edges)
	n.times = make([]float32, ns*ns)
	n.last = make([]int32, ns*ns)
	n.pred = make([][]int32, ns)
	dist := make([]float64, ne)
	best := make([]float64, ns)
	var h heap
	for i := 0; i < ns; i++ {
		pred := make([]int32, ne)
		for e := range dist {
			dist[e] = math.Inf(1)
		}
		relax := func(e int32, d float64, p int32) {
			if d < dist[e] {
				dist[e] = d
				pred[e] = p
				h.push(d, e)
			}
		}
		for _, f := range n.out[i] {
			relax(f, n.entry+n.edges[f].wait+n.edges[f].ride, -1)
		}
		for h.len() > 0 {
			d, e := h.pop()
			if d > dist[e] {
				continue
			}
			cur := n.edges[e]
			for _, f := range n.next[e] {
				relax(f, d+n.edges[f].ride, e<<1)
			}
			for _, f := range n.out[cur.to] {
				fe := n.edges[f]
				c := fe.wait + fe.ride
				if fe.route != cur.route {
					c += n.transfer
				}
				relax(f, d+c, e<<1|1)
			}
			for _, w := range n.walk[cur.to] {
				for _, f := range n.out[w.to] {
					fe := n.edges[f]
					relax(f, d+w.cost+fe.wait+fe.ride, e<<1|1)
				}
			}
		}
		for j := range best {
			best[j] = math.Inf(1)
		}
		row := n.last[i*ns : (i+1)*ns]
		for j := range row {
			row[j] = -1
		}
		for e, d := range dist {
			if to := n.edges[e].to; d < best[to] {
				best[to] = d
				row[to] = int32(e)
			}
		}
		for j := 0; j < ns; j++ {
			t := float32(-1)
			switch {
			case j == i:
				t = 0
				row[j] = -1
			case !math.IsInf(best[j], 1):
				t = float32(best[j])
			}
			n.times[i*ns+j] = t
		}
		n.pred[i] = pred
	}

	n.maxSpeed = 0
	for i := 0; i < ns; i++ {
		for j := 0; j < ns; j++ {
			t, ok := n.Time(i, j)
			if i == j || !ok {
				continue
			}
			d := geo.Distance(n.stations[i].Pos, n.stations[j].Pos)
			if t <= 0 {
				if d > 0 {
					n.maxSpeed = math.Inf(1)
				}
				continue
			}
			n.maxSpeed = math.Max(n.maxSpeed, d/t)
		}
	}
	n.maxSpeed *= maxSpeedMargin

	n.minLat, n.maxLat = math.Inf(1), math.Inf(-1)
	n.minLon, n.maxLon = math.Inf(1), math.Inf(-1)
	for _, s := range n.stations {
		n.minLat = math.Min(n.minLat, s.Pos.Lat)
		n.maxLat = math.Max(n.maxLat, s.Pos.Lat)
		n.minLon = math.Min(n.minLon, s.Pos.Lon)
		n.maxLon = math.Max(n.maxLon, s.Pos.Lon)
	}
	padLat := coverPadMeters / (geo.EarthRadius * math.Pi / 180)
	maxAbsLat := math.Min(89, math.Max(math.Abs(n.minLat), math.Abs(n.maxLat))+padLat)
	padLon := padLat / math.Cos(maxAbsLat*math.Pi/180)
	n.minLat -= padLat
	n.maxLat += padLat
	n.minLon -= padLon
	n.maxLon += padLon
}

// heap is a binary min-heap of (distance, edge state) with lazy deletion.
type heap struct {
	d []float64
	e []int32
}

func (h *heap) len() int { return len(h.d) }

func (h *heap) push(d float64, e int32) {
	h.d = append(h.d, d)
	h.e = append(h.e, e)
	i := len(h.d) - 1
	for i > 0 {
		p := (i - 1) / 2
		if h.d[p] <= h.d[i] {
			break
		}
		h.swap(i, p)
		i = p
	}
}

func (h *heap) pop() (float64, int32) {
	d, e := h.d[0], h.e[0]
	last := len(h.d) - 1
	h.swap(0, last)
	h.d, h.e = h.d[:last], h.e[:last]
	i := 0
	for {
		l, r, m := 2*i+1, 2*i+2, i
		if l < last && h.d[l] < h.d[m] {
			m = l
		}
		if r < last && h.d[r] < h.d[m] {
			m = r
		}
		if m == i {
			break
		}
		h.swap(i, m)
		i = m
	}
	return d, e
}

func (h *heap) swap(i, j int) {
	h.d[i], h.d[j] = h.d[j], h.d[i]
	h.e[i], h.e[j] = h.e[j], h.e[i]
}
