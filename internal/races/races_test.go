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
		{ID: "zeta", Name: "Alpha City Marathon", Slug: "alpha-city-marathon", ShortName: "Alpha City Marathon"}, // metadata name, trimmed
		{ID: "bravo", Name: "bravo half", Slug: "bravo-half", ShortName: "bravo half"},                           // trk fallback, case-insensitive sort
		{ID: "broken", Name: "broken", Slug: "broken", ShortName: "broken"},                                      // parse error falls back to file name
		{ID: "charlie", Name: "charlie", Slug: "charlie", ShortName: "charlie"},                                  // no name falls back to file name
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

func TestSlugsAndShortNames(t *testing.T) {
	c, err := NewCatalog(fstest.MapFS{
		"boston-2025.gpx":   file(`<gpx><metadata><name>Boston Marathon 2025</name></metadata></gpx>`),
		"boston-2026.gpx":   file(`<gpx><metadata><name>Boston Marathon 2026</name></metadata></gpx>`),
		"nyc-2026-blue.gpx": file(`<gpx><metadata><name>NYC Marathon 2026 - Blue Route</name></metadata></gpx>`),
		"mcm_2026.gpx":      file(`<gpx><metadata><name>2026</name></metadata></gpx>`),
		"2024.gpx":          file(`<gpx><metadata><name>Twin Cities Marathon (2024)</name></metadata></gpx>`),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"boston-2026":   {"boston-marathon", "Boston Marathon"},
		"boston-2025":   {"boston-2025", "Boston Marathon"}, // the newer file has the slug
		"nyc-2026-blue": {"nyc-marathon-blue-route", "NYC Marathon - Blue Route"},
		"mcm_2026":      {"2026", "2026"}, // a name that's only a year stays
		"2024":          {"twin-cities-marathon-2024", "Twin Cities Marathon (2024)"},
	}
	for _, r := range c.List() {
		if got := [2]string{r.Slug, r.ShortName}; got != want[r.ID] {
			t.Errorf("%s: got %v, want %v", r.ID, got, want[r.ID])
		}
	}
}

func TestShortNameOf(t *testing.T) {
	for name, want := range map[string]string{
		"Boston Marathon 2025":                 "Boston Marathon",
		"NYC Marathon 2026 - Blue Route":       "NYC Marathon - Blue Route",
		"TCS Sydney Marathon 2025 - 42.195 km": "TCS Sydney Marathon",
		"BMW Berlin-Marathon 2026":             "BMW Berlin-Marathon",
		"Foo City 13.1":                        "Foo City Half Marathon",
		"Foo City 2026 - 26.2 miles":           "Foo City Marathon",
		"Bar Half Marathon 21.1K":              "Bar Half Marathon",
		"Baz 26.2 - Blue Route":                "Baz Marathon - Blue Route",
		"2026":                                 "2026",
	} {
		if got := shortNameOf(name); got != want {
			t.Errorf("%q: got %q, want %q", name, got, want)
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

// Boston is on a Monday, so the UI should default to the weekday schedule.
func TestBostonIsOnAWeekday(t *testing.T) {
	for _, r := range Default.List() {
		want := ""
		if r.ID == "boston-2025" {
			want = "weekday"
		}
		if r.Day != want {
			t.Errorf("%s: day %q, want %q", r.ID, r.Day, want)
		}
	}
}
