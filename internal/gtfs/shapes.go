package gtfs

import (
	"io/fs"
	"sort"
	"strconv"

	"github.com/logie4000/cheersquad/internal/geo"
)

// readShapes returns the points of each needed shape in sequence order.
// shapes.txt is optional; a feed without it yields no shapes.
func readShapes(fsys fs.FS, need map[string]bool) (map[string][]geo.LatLon, error) {
	t, err := openTable(fsys, "shapes.txt", false)
	if err != nil || t == nil {
		return nil, err
	}
	defer t.close()
	if err := t.requireCols("shape_id", "shape_pt_lat", "shape_pt_lon", "shape_pt_sequence"); err != nil {
		return nil, err
	}
	type pt struct {
		seq int
		p   geo.LatLon
	}
	raw := map[string][]pt{}
	for {
		ok, err := t.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		id := t.get("shape_id")
		if !need[id] {
			continue
		}
		lat, err1 := strconv.ParseFloat(t.get("shape_pt_lat"), 64)
		lon, err2 := strconv.ParseFloat(t.get("shape_pt_lon"), 64)
		seq, err3 := strconv.Atoi(t.get("shape_pt_sequence"))
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		raw[id] = append(raw[id], pt{seq, geo.LatLon{Lat: lat, Lon: lon}})
	}
	shapes := make(map[string][]geo.LatLon, len(raw))
	for id, pts := range raw {
		sort.SliceStable(pts, func(i, j int) bool { return pts[i].seq < pts[j].seq })
		line := make([]geo.LatLon, len(pts))
		for i, p := range pts {
			line[i] = p.p
		}
		shapes[id] = line
	}
	return shapes, nil
}

// shapePos is a point on a shape: f along segment seg.
type shapePos struct {
	seg int
	f   float64
}

// projectStops places each stop of a trip on its shape, moving only forward
// so loops and out-and-back lines map each stop to the right pass. Among
// segments ahead it takes the earliest one within loopSlack of the closest.
func projectStops(shape []geo.LatLon, stops []geo.LatLon) []shapePos {
	const loopSlack = 30.0 // meters
	out := make([]shapePos, len(stops))
	from := 0
	for k, p := range stops {
		type cand struct {
			seg     int
			f, dist float64
		}
		var cs []cand
		minDist := -1.0
		for s := from; s+1 < len(shape); s++ {
			f, d := geo.ProjectOnSegment(p, shape[s], shape[s+1])
			cs = append(cs, cand{s, f, d})
			if minDist < 0 || d < minDist {
				minDist = d
			}
		}
		best := shapePos{seg: from}
		for _, c := range cs {
			if c.dist <= minDist+loopSlack {
				best = shapePos{c.seg, c.f}
				break
			}
		}
		// Never step back within a segment either.
		if k > 0 && best.seg == out[k-1].seg && best.f < out[k-1].f {
			best.f = out[k-1].f
		}
		out[k] = best
		from = best.seg
	}
	return out
}

// cut returns the shape between two positions, endpoints included.
func cut(shape []geo.LatLon, a, b shapePos) []geo.LatLon {
	at := func(p shapePos) geo.LatLon {
		return geo.Interpolate(shape[p.seg], shape[p.seg+1], p.f)
	}
	path := []geo.LatLon{at(a)}
	for v := a.seg + 1; v <= b.seg; v++ {
		path = append(path, shape[v])
	}
	if end := at(b); end != path[len(path)-1] {
		path = append(path, end)
	}
	return path
}

// edgePaths cuts each sampled edge's geometry out of its trip's shape.
func edgePaths(samples []edgeSample, shaped map[int32][]stopTime, trips []tripRow, shapes map[string][]geo.LatLon, pos func(stop int32) geo.LatLon) [][]geo.LatLon {
	proj := map[int32][]shapePos{}
	paths := make([][]geo.LatLon, len(samples))
	for e, s := range samples {
		if s.trip < 0 {
			continue
		}
		shape := shapes[trips[s.trip].shape]
		if len(shape) < 2 {
			continue
		}
		ps, ok := proj[s.trip]
		if !ok {
			sts := shaped[s.trip]
			pts := make([]geo.LatLon, len(sts))
			for i, st := range sts {
				pts[i] = pos(st.stop)
			}
			ps = projectStops(shape, pts)
			proj[s.trip] = ps
		}
		paths[e] = cut(shape, ps[s.k], ps[s.k+1])
	}
	return paths
}
