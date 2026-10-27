// Package geo holds the small amount of spherical and planar geometry the
// planner needs. All distances are meters.
package geo

import "math"

// EarthRadius is the mean Earth radius in meters.
const EarthRadius = 6371008.8

// LatLon is a WGS84 coordinate in decimal degrees.
type LatLon struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

func rad(deg float64) float64 { return deg * math.Pi / 180 }

// Distance returns the great-circle (haversine) distance between a and b.
func Distance(a, b LatLon) float64 {
	dLat := rad(b.Lat - a.Lat)
	dLon := rad(b.Lon - a.Lon)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(a.Lat))*math.Cos(rad(b.Lat))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * EarthRadius * math.Asin(math.Min(1, math.Sqrt(h)))
}

// Interpolate returns the point a fraction f of the way from a to b. It is
// linear in lat/lon, which is accurate for the short segments of a GPX track.
func Interpolate(a, b LatLon, f float64) LatLon {
	return LatLon{Lat: a.Lat + (b.Lat-a.Lat)*f, Lon: a.Lon + (b.Lon-a.Lon)*f}
}

// project maps p to local planar meters around origin (equirectangular).
func project(origin, p LatLon) (x, y float64) {
	x = rad(p.Lon-origin.Lon) * math.Cos(rad(origin.Lat)) * EarthRadius
	y = rad(p.Lat-origin.Lat) * EarthRadius
	return x, y
}

// SegmentIntersection reports whether segments a1-a2 and b1-b2 properly
// cross, and if so the fraction t along a1-a2 where they meet. Collinear or
// merely touching segments do not count as crossing.
func SegmentIntersection(a1, a2, b1, b2 LatLon) (t float64, ok bool) {
	ax, ay := project(a1, a2)
	bx1, by1 := project(a1, b1)
	bx2, by2 := project(a1, b2)
	dx, dy := bx2-bx1, by2-by1
	den := ax*dy - ay*dx
	if den == 0 {
		return 0, false
	}
	t = (bx1*dy - by1*dx) / den
	u := (bx1*ay - by1*ax) / den
	if t <= 0 || t >= 1 || u <= 0 || u >= 1 {
		return 0, false
	}
	return t, true
}

// ProjectOnSegment returns the fraction along a-b of the point closest to p
// and the distance from p to that point.
func ProjectOnSegment(p, a, b LatLon) (f, dist float64) {
	bx, by := project(a, b)
	px, py := project(a, p)
	l2 := bx*bx + by*by
	if l2 > 0 {
		f = math.Max(0, math.Min(1, (px*bx+py*by)/l2))
	}
	return f, Distance(p, Interpolate(a, b, f))
}
