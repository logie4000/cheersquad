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

// realIndex is the app's own index.html, so the tests catch a change to it
// that Render no longer matches.
func realIndex(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../web/src/index.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fixture(t *testing.T) (string, *races.Catalog) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(realIndex(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("js"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "fonts"), 0o755); err != nil {
		t.Fatal(err)
	}
	cat, err := races.NewCatalog(fstest.MapFS{
		"boston-2025.gpx": {Data: []byte(strings.Replace(gpxNamed, "%s", "Boston &amp; Back 2025", 1))},
		"austin.gpx":      {Data: []byte(strings.Replace(gpxNamed, "%s", "Austin", 1))},
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

func TestRealIndex(t *testing.T) {
	index := realIndex(t)
	for name, re := range map[string]interface{ FindAllStringIndex(string, int) [][]int }{
		"title": reTitle, "base": reBase, "description": reDesc, "og:title": reOGTitle,
		"og:description": reOGDesc, "canonical": reCanon, "og:url": reOGURL, "intro": reIntro,
	} {
		if n := len(re.FindAllStringIndex(index, -1)); n != 1 {
			t.Errorf("%s matches %d times in web/src/index.html, want 1", name, n)
		}
	}
}

func contains(t *testing.T, label, body string, want ...string) {
	t.Helper()
	for _, s := range want {
		if !strings.Contains(body, s) {
			t.Errorf("%s: missing %s", label, s)
		}
	}
}

func TestHomePage(t *testing.T) {
	dir, cat := fixture(t)
	h := Handler(Config{Dir: dir, Races: cat})
	for _, path := range []string{"/", "/index.html"} {
		w := get(h, path, proxied)
		if w.Code != 200 {
			t.Fatalf("%s: status %d", path, w.Code)
		}
		body := w.Body.String()
		contains(t, path, body,
			`<title>Cheer Squad: marathon spectator planner</title>`,
			`<base href="./"`,
			`<link rel="canonical" href="https://www.example.org/cheersquad/"`,
			`content="https://www.example.org/cheersquad/og-image.png"`,
			`"url": "https://www.example.org/cheersquad/"`,
			`<li><a href="race/austin">Austin</a></li><li><a href="race/boston">Boston &amp; Back</a></li>`,
		)
		if strings.Contains(body, URLToken) {
			t.Errorf("%s: %s left in the page", path, URLToken)
		}
	}
}

func TestRacePage(t *testing.T) {
	dir, cat := fixture(t)
	w := get(Handler(Config{Dir: dir, Races: cat}), "/race/boston", proxied)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	contains(t, "race", body,
		`<title>Where to watch the Boston &amp; Back | Cheer Squad</title>`,
		`<base href="../"`,
		`<link rel="canonical" href="https://www.example.org/cheersquad/race/boston"`,
		`content="https://www.example.org/cheersquad/race/boston"`,
		`content="Plan where to stand to cheer your runner at the Boston &amp; Back:`,
		`<h1>Where to watch the Boston &amp; Back</h1>`,
		`<li><a href="race/austin">Austin</a></li></ul>`,
	)
	if strings.Contains(body, `href="race/boston"`) {
		t.Error("race page links to itself")
	}
	if w := get(Handler(Config{Dir: dir, Races: cat}), "/race/nope", nil); w.Code != 404 {
		t.Errorf("unknown race: status %d, want 404", w.Code)
	}
}

func TestConfiguredURLWins(t *testing.T) {
	dir, cat := fixture(t)
	h := Handler(Config{Dir: dir, Races: cat, URL: "https://cheer.example/"})
	contains(t, "configured", get(h, "/race/austin", proxied).Body.String(), `href="https://cheer.example/race/austin"`)
}

func TestSitemap(t *testing.T) {
	dir, cat := fixture(t)
	w := get(Handler(Config{Dir: dir, Races: cat}), "/sitemap.xml", proxied)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	contains(t, "sitemap", w.Body.String(),
		"<loc>https://www.example.org/cheersquad/</loc>",
		"<loc>https://www.example.org/cheersquad/race/austin</loc>",
		"<loc>https://www.example.org/cheersquad/race/boston</loc>",
	)
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
	for _, path := range []string{"/nope", "/some/route", "/fonts/", "/race/", "/../etc/passwd"} {
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
