package gpx

import (
	"math"
	"strings"
	"testing"

	"github.com/logie4000/cheersquad/internal/geo"
)

const sample = `<?xml version="1.0"?>
<gpx version="1.1" creator="test" xmlns="http://www.topografix.com/GPX/1/1">
  <trk><trkseg>
    <trkpt lat="0" lon="0"></trkpt>
    <trkpt lat="0.01" lon="0"></trkpt>
    <trkpt lat="0.01" lon="0"></trkpt>
  </trkseg><trkseg>
    <trkpt lat="0.02" lon="0"></trkpt>
  </trkseg></trk>
</gpx>`

var degLat = geo.Distance(geo.LatLon{}, geo.LatLon{Lat: 1})

func TestParseJoinsSegmentsAndDropsDuplicates(t *testing.T) {
	tr, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Points) != 3 {
		t.Fatalf("got %d points, want 3", len(tr.Points))
	}
	if math.Abs(tr.Length()-0.02*degLat) > 0.01 {
		t.Fatalf("length %f", tr.Length())
	}
}

func TestParseRouteFallback(t *testing.T) {
	doc := `<gpx><rte><rtept lat="0" lon="0"/><rtept lat="0.01" lon="0"/></rte></gpx>`
	tr, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Points) != 2 {
		t.Fatalf("got %d points", len(tr.Points))
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	if _, err := Parse(strings.NewReader(`<gpx><trk><trkseg><trkpt lat="1" lon="1"/></trkseg></trk></gpx>`)); err == nil {
		t.Fatal("expected error for single-point course")
	}
	if _, err := Parse(strings.NewReader(`not xml`)); err == nil {
		t.Fatal("expected error for bad XML")
	}
}

func TestParseRejectsInvalidCoordinates(t *testing.T) {
	for _, pt := range []string{
		`lat="NaN" lon="0"`, `lat="0" lon="Inf"`, `lat="91" lon="0"`, `lat="0" lon="-181"`,
	} {
		doc := `<gpx><trk><trkseg><trkpt lat="0" lon="0"/><trkpt ` + pt + `/></trkseg></trk></gpx>`
		if _, err := Parse(strings.NewReader(doc)); err == nil {
			t.Errorf("%s: expected error", pt)
		}
	}
}

func TestParseIgnoresEntities(t *testing.T) {
	// encoding/xml neither fetches external entities nor expands DTD
	// entities, so an XXE or entity-expansion file is just a parse error.
	doc := `<?xml version="1.0"?>
<!DOCTYPE gpx [<!ENTITY a "0.01"><!ENTITY xxe SYSTEM "file:///etc/passwd">]>
<gpx><trk><trkseg><trkpt lat="0" lon="0"/><trkpt lat="&a;" lon="&xxe;"/></trkseg></trk></gpx>`
	if _, err := Parse(strings.NewReader(doc)); err == nil {
		t.Fatal("expected entity references to be rejected")
	}
}

func TestCalibrateAndPointAt(t *testing.T) {
	tr, _ := Parse(strings.NewReader(sample))
	tr.Calibrate(2000)
	if math.Abs(tr.Length()-2000) > 1e-9 {
		t.Fatalf("length %f", tr.Length())
	}
	// Halfway along the calibrated course is the middle point.
	p := tr.PointAt(1000)
	if math.Abs(p.Lat-0.01) > 1e-9 {
		t.Fatalf("PointAt(1000) = %+v", p)
	}
	if got := tr.PointAt(-5); got != tr.Points[0] {
		t.Fatalf("PointAt clamps low: %+v", got)
	}
	if got := tr.PointAt(1e9); got != tr.Points[len(tr.Points)-1] {
		t.Fatalf("PointAt clamps high: %+v", got)
	}
}

func TestReadName(t *testing.T) {
	cases := map[string]struct{ doc, want string }{
		"metadata wins": {`<gpx xmlns="http://www.topografix.com/GPX/1/1"><metadata><name>
  City Marathon 2026 </name></metadata><trk><name>Track</name></trk></gpx>`, "City Marathon 2026"},
		"track fallback":  {`<gpx><metadata><name> </name></metadata><trk><name></name></trk><trk><name>Second</name></trk><rte><name>Route</name></rte></gpx>`, "Second"},
		"route fallback":  {`<gpx><rte><name>Route Only</name></rte></gpx>`, "Route Only"},
		"no name":         {sample, ""},
		"point name only": {`<gpx><trk><trkseg><trkpt lat="0" lon="0"><name>pt</name></trkpt></trkseg></trk></gpx>`, ""},
	}
	for label, c := range cases {
		got, err := ReadName(strings.NewReader(c.doc))
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q want %q", label, got, c.want)
		}
	}
	if _, err := ReadName(strings.NewReader("<gpx><trk>")); err == nil {
		t.Error("truncated file should error")
	}
}
