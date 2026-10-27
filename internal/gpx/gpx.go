// Package gpx parses a course from a GPX file and measures it.
//
// It uses encoding/xml rather than a GPX library: we only need track (or
// route) point coordinates, and that is a few lines of struct tags.
package gpx

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/logie4000/cheersquad/internal/geo"
)

// Track is a course polyline with cumulative distance at each point.
type Track struct {
	Points []geo.LatLon
	// Cum[i] is the distance in meters from Points[0] to Points[i].
	Cum []float64
}

type point struct {
	Lat float64 `xml:"lat,attr"`
	Lon float64 `xml:"lon,attr"`
}

type document struct {
	Tracks []struct {
		Segments []struct {
			Points []point `xml:"trkpt"`
		} `xml:"trkseg"`
	} `xml:"trk"`
	Routes []struct {
		Points []point `xml:"rtept"`
	} `xml:"rte"`
}

// Parse reads all track points (in file order, segments joined). If the file
// has no tracks it falls back to route points.
func Parse(r io.Reader) (*Track, error) {
	var doc document
	if err := xml.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("gpx: %w", err)
	}
	var pts []geo.LatLon
	for _, trk := range doc.Tracks {
		for _, seg := range trk.Segments {
			for _, p := range seg.Points {
				pts = append(pts, geo.LatLon{Lat: p.Lat, Lon: p.Lon})
			}
		}
	}
	if len(pts) == 0 {
		for _, rte := range doc.Routes {
			for _, p := range rte.Points {
				pts = append(pts, geo.LatLon{Lat: p.Lat, Lon: p.Lon})
			}
		}
	}
	return New(pts)
}

// New builds a Track from points, dropping consecutive duplicates.
func New(pts []geo.LatLon) (*Track, error) {
	t := &Track{}
	for _, p := range pts {
		if n := len(t.Points); n > 0 && t.Points[n-1] == p {
			continue
		}
		d := 0.0
		if n := len(t.Points); n > 0 {
			d = t.Cum[n-1] + geo.Distance(t.Points[n-1], p)
		}
		t.Points = append(t.Points, p)
		t.Cum = append(t.Cum, d)
	}
	if len(t.Points) < 2 {
		return nil, errors.New("gpx: course needs at least two distinct points")
	}
	return t, nil
}

// Length is the total course length in meters.
func (t *Track) Length() float64 { return t.Cum[len(t.Cum)-1] }

// Calibrate scales cumulative distances so the course measures certified
// meters. GPX tracks usually come out a little long or short versus the
// certified distance; scaling keeps mile markers and pace math consistent.
func (t *Track) Calibrate(certified float64) {
	if certified <= 0 {
		return
	}
	k := certified / t.Length()
	for i := range t.Cum {
		t.Cum[i] *= k
	}
}

// Locate returns the segment index i and fraction f such that distance d lies
// between Points[i] and Points[i+1]. d is clamped to the course.
func (t *Track) Locate(d float64) (i int, f float64) {
	if d <= 0 {
		return 0, 0
	}
	if d >= t.Length() {
		return len(t.Points) - 2, 1
	}
	i = sort.SearchFloat64s(t.Cum, d) - 1
	if i < 0 {
		i = 0
	}
	seg := t.Cum[i+1] - t.Cum[i]
	if seg > 0 {
		f = (d - t.Cum[i]) / seg
	}
	return i, f
}

// PointAt returns the position at distance d along the course.
func (t *Track) PointAt(d float64) geo.LatLon {
	i, f := t.Locate(d)
	return geo.Interpolate(t.Points[i], t.Points[i+1], f)
}
