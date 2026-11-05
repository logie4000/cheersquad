// Package site serves the built web UI, filling in the parts of index.html
// that search engines and link previews read before any JavaScript runs:
// the page's title, description and public URL (canonical link, Open Graph,
// structured data) and a crawlable intro. Each preloaded race gets its own
// page at race/<slug> (the race without its year), which the app opens with that race selected. It also
// serves sitemap.xml.
package site

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/logie4000/cheersquad/internal/races"
)

// URLToken stands for the app's public base URL (ending in "/") in
// index.html, e.g. "https://www.example.org/cheersquad/".
const URLToken = "__SITE_URL__"

// RacePrefix is the path of the race pages, relative to the app.
const RacePrefix = "race/"

// Config is what Handler needs.
type Config struct {
	// Dir holds the built UI (index.html and its assets).
	Dir string
	// URL is the app's public base URL. Empty means work it out from
	// each request (Host, X-Forwarded-Proto, X-Forwarded-Prefix).
	URL string
	// Races are listed on the page; nil means the embedded ones.
	Races *races.Catalog
}

// Page is what differs between the home page and a race page.
type Page struct {
	Title       string
	Description string
	// Path is the page's URL relative to the app, "" for the home page.
	Path string
	// Base is the <base href> that makes the app's relative URLs work
	// from Path.
	Base string
	// Intro is the HTML shown inside <app-root> until the app starts.
	Intro string
}

const (
	homeTitle       = "Cheer Squad: marathon spectator planner"
	homeDescription = "Free marathon spectator planner. Pick a race or upload a GPX course, enter your runner's goal time, and get the spots to stand so you see them as many times as possible, by foot, bike, transit or car."
)

// Home is the app's front page.
func Home(list []races.Race) Page {
	return Page{
		Title:       homeTitle,
		Description: homeDescription,
		Base:        "./",
		Intro: `<div class="intro"><h1>Cheer Squad</h1>` +
			`<p>Plan where to stand so you see your runner as many times as possible.</p>` +
			`<p>Cheer Squad is a free marathon spectator planner. Pick a preloaded race or upload the course as a GPX file, ` +
			`enter your runner's goal finish time and pacing, and say when you can set off and how you'll get around: ` +
			`walking, bike, transit or car. It works out where your runner will be and when, then plans a route of ` +
			`cheering spots you can actually reach in time, with travel directions between them, a map, and a printable plan.</p>` +
			`<h2>Preloaded races</h2>` + raceLinks(list, "") + noscript + `</div>`,
	}
}

// RacePage is the page for one preloaded race.
func RacePage(r races.Race, list []races.Race) Page {
	name := html.EscapeString(r.ShortName)
	return Page{
		Title:       fmt.Sprintf("Where to watch the %s | Cheer Squad", r.ShortName),
		Description: fmt.Sprintf("Plan where to stand to cheer your runner at the %s: the spots you can reach in time on foot, by bike, transit or car, with a map and a printable plan. Free.", r.ShortName),
		Path:        RacePrefix + r.Slug,
		Base:        "../",
		Intro: `<div class="intro"><h1>Where to watch the ` + name + `</h1>` +
			`<p>Cheering at the ` + name + `? Cheer Squad plans where to stand so you see your runner as many times as possible.</p>` +
			`<p>The ` + name + ` course is already loaded. Enter your runner's goal finish time and when their wave starts, ` +
			`say when you can set off and whether you're walking, cycling, taking transit or driving, and Cheer Squad works out ` +
			`when your runner reaches each part of the course and which cheering spots you can get to in time. ` +
			`You get the spots on a map, travel directions between them, and a plan to print or save as a PDF.</p>` +
			`<h2>Other races</h2>` + raceLinks(list, r.ID) +
			`<p><a href="./">Plan for any course from a GPX file</a></p>` + noscript + `</div>`,
	}
}

const noscript = `<noscript><p>Cheer Squad needs JavaScript to plan your day.</p></noscript>`

// raceLinks lists the races as links to their pages, leaving out skip.
// The hrefs are relative to the page's <base>, i.e. the app.
func raceLinks(list []races.Race, skip string) string {
	var b strings.Builder
	b.WriteString("<ul>")
	for _, r := range list {
		if r.ID == skip {
			continue
		}
		b.WriteString(`<li><a href="` + RacePrefix + html.EscapeString(url.PathEscape(r.Slug)) + `">` + html.EscapeString(r.ShortName) + "</a></li>")
	}
	b.WriteString("</ul>")
	return b.String()
}

// Handler serves Dir. "/" and "/index.html" get the filled-in index.html,
// "/race/<slug>" the same for a race, "/sitemap.xml" a sitemap, other
// existing files as they are, and anything else a real 404.
func Handler(cfg Config) http.Handler {
	catalog := cfg.Races
	if catalog == nil {
		catalog = races.Default
	}
	list := catalog.List()
	pages := map[string]Page{"/": Home(list), "/index.html": Home(list)}
	for _, r := range list {
		pages["/"+RacePrefix+r.Slug] = RacePage(r, list)
	}
	files := http.FileServer(http.Dir(cfg.Dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := cfg.URL
		if base == "" {
			base = BaseURL(r)
		}
		if p, ok := pages[r.URL.Path]; ok {
			b, err := os.ReadFile(filepath.Join(cfg.Dir, "index.html"))
			if err != nil {
				http.Error(w, "CheerSquad UI not built yet; the API is at /api/", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// The page names the latest asset hashes, so always revalidate.
			w.Header().Set("Cache-Control", "no-cache")
			w.Write([]byte(Render(string(b), p, base)))
			return
		}
		if r.URL.Path == "/sitemap.xml" {
			paths := []string{""}
			for _, r := range list {
				paths = append(paths, RacePrefix+r.Slug)
			}
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.Write([]byte(Sitemap(base, paths)))
			return
		}
		// Unknown paths and folders get a 404 rather than another copy of
		// the page or a file listing.
		if fi, err := os.Stat(filepath.Join(cfg.Dir, filepath.Clean("/"+r.URL.Path))); err != nil || fi.IsDir() {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// The parts of index.html that Render replaces. Each must match exactly
// once (TestRealIndex checks web/src/index.html).
var (
	reTitle   = regexp.MustCompile(`<title>[^<]*</title>`)
	reBase    = regexp.MustCompile(`(<base\s+href=")[^"]*(")`)
	reDesc    = regexp.MustCompile(`(<meta\s+name="description"\s+content=")[^"]*(")`)
	reOGTitle = regexp.MustCompile(`(<meta\s+property="og:title"\s+content=")[^"]*(")`)
	reOGDesc  = regexp.MustCompile(`(<meta\s+property="og:description"\s+content=")[^"]*(")`)
	reCanon   = regexp.MustCompile(`(<link\s+rel="canonical"\s+href=")[^"]*(")`)
	reOGURL   = regexp.MustCompile(`(<meta\s+property="og:url"\s+content=")[^"]*(")`)
	reIntro   = regexp.MustCompile(`(?s)<div class="intro">.*?</div>`)
)

// Render fills index.html in for page p, with the app at base.
func Render(index string, p Page, base string) string {
	pageURL := html.EscapeString(base + p.Path)
	index = replace(index, reTitle, "<title>"+html.EscapeString(p.Title)+"</title>")
	index = replaceAttr(index, reBase, p.Base)
	index = replaceAttr(index, reDesc, html.EscapeString(p.Description))
	index = replaceAttr(index, reOGTitle, html.EscapeString(p.Title))
	index = replaceAttr(index, reOGDesc, html.EscapeString(p.Description))
	index = replaceAttr(index, reCanon, pageURL)
	index = replaceAttr(index, reOGURL, pageURL)
	index = replace(index, reIntro, p.Intro)
	return strings.ReplaceAll(index, URLToken, html.EscapeString(base))
}

func replace(s string, re *regexp.Regexp, with string) string {
	return re.ReplaceAllLiteralString(s, with)
}

// replaceAttr swaps the value between re's two groups for v.
func replaceAttr(s string, re *regexp.Regexp, v string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		g := re.FindStringSubmatch(m)
		return g[1] + v + g[2]
	})
}

// Sitemap lists the app's pages, given relative to base.
func Sitemap(base string, paths []string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range paths {
		b.WriteString("  <url><loc>" + html.EscapeString(base+p) + "</loc></url>\n")
	}
	b.WriteString("</urlset>\n")
	return b.String()
}

var (
	hostRe   = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)
	prefixRe = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)*/?$`)
)

// BaseURL is the public URL the request came in on, as seen through the
// host Nginx: its scheme (X-Forwarded-Proto), host and path prefix
// (X-Forwarded-Prefix). Values that don't look like a host or a path are
// ignored, so a forged header can't put markup in the page.
func BaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := strings.ToLower(r.Header.Get("X-Forwarded-Proto")); p == "https" || p == "http" {
		scheme = p
	}
	host := r.Host
	if !hostRe.MatchString(host) {
		host = "localhost"
	}
	prefix := r.Header.Get("X-Forwarded-Prefix")
	if !prefixRe.MatchString(prefix) {
		prefix = ""
	}
	u := url.URL{Scheme: scheme, Host: host, Path: strings.TrimSuffix(prefix, "/") + "/"}
	return u.String()
}
