package races

import (
	"testing"
	"testing/fstest"
)

func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

func fixture() fstest.MapFS {
	return fstest.MapFS{
		"zeta.gpx":       file(`<gpx><metadata><name> Alpha City Marathon </name></metadata><trk><name>ignored</name></trk></gpx>`),
		"bravo.gpx":      file(`<gpx><trk><name>bravo half</name></trk></gpx>`),
		"charlie.gpx":    file(`<gpx><trk><trkseg><trkpt lat="0" lon="0"/></trkseg></trk></gpx>`),
		"broken.gpx":     file(`<gpx><metadata>`),
		"README.md":      file("# not a race"),
		"notes.txt":      file("<gpx><metadata><name>Nope</name></metadata></gpx>"),
		"sub/nested.gpx": file(`<gpx><metadata><name>Nested</name></metadata></gpx>`),
	}
}

func TestCatalogList(t *testing.T) {
	c, err := NewCatalog(fixture())
	if err != nil {
		t.Fatal(err)
	}
	want := []Race{
		{"zeta", "Alpha City Marathon"}, // metadata name, trimmed
		{"bravo", "bravo half"},         // trk fallback, case-insensitive sort
		{"broken", "broken"},            // parse error falls back to file name
		{"charlie", "charlie"},          // no name falls back to file name
	}
	got := c.List()
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %v want %v", i, got[i], want[i])
		}
	}
}

func TestCatalogGPX(t *testing.T) {
	c, _ := NewCatalog(fixture())
	if b, ok := c.GPX("bravo"); !ok || string(b) != `<gpx><trk><name>bravo half</name></trk></gpx>` {
		t.Fatalf("bravo: ok=%v body=%q", ok, b)
	}
	for _, id := range []string{"", "missing", "bravo.gpx", "README", "notes", "../bravo", "sub/nested", "nested"} {
		if _, ok := c.GPX(id); ok {
			t.Errorf("id %q should not resolve", id)
		}
	}
}

func TestEmptyCatalog(t *testing.T) {
	c, err := NewCatalog(fstest.MapFS{"README.md": file("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if l := c.List(); l == nil || len(l) != 0 {
		t.Fatalf("want empty non-nil list, got %#v", l)
	}
}

func TestDefaultLoads(t *testing.T) {
	if Default == nil || Default.List() == nil {
		t.Fatal("default catalog should load")
	}
}
