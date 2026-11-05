package site

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/logie4000/cheersquad/internal/races"
)

const gpxNamed = `<?xml version="1.0"?><gpx version="1.1" creator="t" xmlns="http://www.topografix.com/GPX/1/1"><metadata><name>%s</name></metadata><trk><trkseg><trkpt lat="0" lon="0"></trkpt></trkseg></trk></gpx>`

func fixture(t *testing.T) (string, *races.Catalog) {
	t.Helper()
	dir := t.TempDir()
	index := `<link rel="canonical" href="__SITE_URL__"><script type="application/ld+json">{"url":"__SITE_URL__"}</script><ul data-races></ul>`
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("js"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "fonts"), 0o755); err != nil {
		t.Fatal(err)
	}
	cat, err := races.NewCatalog(fstest.MapFS{
		"b.gpx": {Data: []byte(strings.Replace(gpxNamed, "%s", "Boston &amp; Back", 1))},
		"a.gpx": {Data: []byte(strings.Replace(gpxNamed, "%s", "Austin", 1))},
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir, cat
}

func get(h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Host = "www.example.org"
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

var proxied = map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Prefix": "/cheersquad"}

func TestIndexFilledIn(t *testing.T) {
	dir, cat := fixture(t)
	h := Handler(Config{Dir: dir, Races: cat})
	for _, path := range []string{"/", "/index.html"} {
		w := get(h, path, proxied)
		if w.Code != 200 {
			t.Fatalf("%s: status %d", path, w.Code)
		}
		body := w.Body.String()
		want := `<link rel="canonical" href="https://www.example.org/cheersquad/"><script type="application/ld+json">{"url":"https://www.example.org/cheersquad/"}</script>` +
			`<ul data-races><li>Austin</li><li>Boston &amp; Back</li></ul>`
		if body != want {
			t.Errorf("%s:\n got %s\nwant %s", path, body, want)
		}
	}
}

func TestConfiguredURLWins(t *testing.T) {
	dir, cat := fixture(t)
	h := Handler(Config{Dir: dir, Races: cat, URL: "https://cheer.example/"})
	if body := get(h, "/", proxied).Body.String(); !strings.Contains(body, `href="https://cheer.example/"`) {
		t.Errorf("got %s", body)
	}
}

func TestSitemap(t *testing.T) {
	dir, cat := fixture(t)
	w := get(Handler(Config{Dir: dir, Races: cat}), "/sitemap.xml", proxied)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<loc>https://www.example.org/cheersquad/</loc>") {
		t.Errorf("status %d body %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("content type %q", ct)
	}
}

func TestFilesAndNotFound(t *testing.T) {
	dir, cat := fixture(t)
	h := Handler(Config{Dir: dir, Races: cat})
	if w := get(h, "/app.js", nil); w.Code != 200 || w.Body.String() != "js" {
		t.Errorf("app.js: %d %q", w.Code, w.Body.String())
	}
	for _, path := range []string{"/nope", "/some/route", "/fonts/", "/../etc/passwd"} {
		if w := get(h, path, nil); w.Code != 404 {
			t.Errorf("%s: status %d, want 404", path, w.Code)
		}
	}
}

func TestBaseURL(t *testing.T) {
	cases := []struct {
		host string
		hdr  map[string]string
		want string
	}{
		{"localhost:8080", nil, "http://localhost:8080/"},
		{"www.example.org", proxied, "https://www.example.org/cheersquad/"},
		{"www.example.org", map[string]string{"X-Forwarded-Prefix": "/cheersquad/"}, "http://www.example.org/cheersquad/"},
		// Forged values fall back rather than reach the page.
		{`evil"><script>`, map[string]string{"X-Forwarded-Proto": "javascript", "X-Forwarded-Prefix": `/"><b>`}, "http://localhost/"},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = c.host
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		if got := BaseURL(req); got != c.want {
			t.Errorf("host %q %v: got %q, want %q", c.host, c.hdr, got, c.want)
		}
	}
}
