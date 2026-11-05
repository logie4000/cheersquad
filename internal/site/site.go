// Package site serves the built web UI, filling in the parts of index.html
// that search engines and link previews read before any JavaScript runs:
// the page's public URL (canonical link, Open Graph, structured data) and
// the list of preloaded races. It also serves sitemap.xml.
package site

import (
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

// RacesMarker is the empty list in index.html that gets one item per
// preloaded race.
const RacesMarker = "<ul data-races></ul>"

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

// Handler serves Dir. "/" and "/index.html" get the filled-in index.html,
// "/sitemap.xml" a one-page sitemap, other existing files as they are, and
// anything else a real 404 (the app has no client-side routes, so serving
// index.html there would only make duplicate pages for crawlers).
func Handler(cfg Config) http.Handler {
	catalog := cfg.Races
	if catalog == nil {
		catalog = races.Default
	}
	list := raceList(catalog)
	files := http.FileServer(http.Dir(cfg.Dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := cfg.URL
		if base == "" {
			base = BaseURL(r)
		}
		switch r.URL.Path {
		case "/", "/index.html":
			b, err := os.ReadFile(filepath.Join(cfg.Dir, "index.html"))
			if err != nil {
				http.Error(w, "CheerSquad UI not built yet; the API is at /api/", http.StatusNotFound)
				return
			}
			page := strings.ReplaceAll(string(b), URLToken, html.EscapeString(base))
			page = strings.Replace(page, RacesMarker, list, 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// The page names the latest asset hashes, so always revalidate.
			w.Header().Set("Cache-Control", "no-cache")
			w.Write([]byte(page))
			return
		case "/sitemap.xml":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.Write([]byte(Sitemap(base)))
			return
		}
		// Folders get a 404 too, rather than a file listing.
		if fi, err := os.Stat(filepath.Join(cfg.Dir, filepath.Clean("/"+r.URL.Path))); err != nil || fi.IsDir() {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// Sitemap lists the app's one page.
func Sitemap(base string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	b.WriteString("  <url><loc>" + html.EscapeString(base) + "</loc></url>\n")
	b.WriteString("</urlset>\n")
	return b.String()
}

func raceList(c *races.Catalog) string {
	var b strings.Builder
	b.WriteString("<ul data-races>")
	for _, r := range c.List() {
		b.WriteString("<li>" + html.EscapeString(r.Name) + "</li>")
	}
	b.WriteString("</ul>")
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
