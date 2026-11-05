// Package races serves the preloaded race courses shipped in data/.
//
// Each data/<id>.gpx file is one race. Its display name comes from the GPX
// itself (see gpx.ReadName), falling back to the id.
package races

import (
	"bytes"
	"embed"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/logie4000/cheersquad/internal/gpx"
)

// Embedding the directory (not data/*.gpx) keeps the build working when no
// GPX files are present; README.md is skipped by NewCatalog.
//
//go:embed data
var embedded embed.FS

// Race is one entry in the race dropdown.
type Race struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Slug names the race's page (races/<slug>): ShortName as an address
	// ("boston-marathon"), so it stays the same when a course file is
	// replaced by next year's. When two files share one, the later year gets it and the
	// other keeps its id.
	Slug string `json:"slug"`
	// ShortName is Name without the year, and with a standard distance
	// swapped for the race type, for page titles and links.
	ShortName string `json:"shortName"`
	// Day is the race's transit timetable when it is not Sunday's
	// ("saturday" or "weekday"), so the UI can preselect it.
	Day string `json:"day,omitempty"`
}

// raceDays are the races not run on a Sunday, by id prefix. Boston is on
// Patriots' Day, a Monday, and the T runs its weekday schedule.
var raceDays = map[string]string{
	"boston-": "weekday",
}

func dayOf(id string) string {
	for prefix, day := range raceDays {
		if strings.HasPrefix(id, prefix) {
			return day
		}
	}
	return ""
}

// Catalog is an immutable set of races, read once at construction.
type Catalog struct {
	list []Race
	gpx  map[string][]byte
}

// NewCatalog reads every top-level *.gpx file in fsys. A file whose name
// cannot be read falls back to its id rather than failing.
func NewCatalog(fsys fs.FS) (*Catalog, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	c := &Catalog{list: []Race{}, gpx: map[string][]byte{}}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(path.Ext(e.Name()), ".gpx") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), path.Ext(e.Name()))
		if id == "" {
			continue
		}
		data, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		name, err := gpx.ReadName(bytes.NewReader(data))
		if err != nil || name == "" {
			name = id
		}
		c.list = append(c.list, Race{ID: id, Name: name, ShortName: shortNameOf(name), Day: dayOf(id)})
		c.gpx[id] = data
	}
	assignSlugs(c.list)
	sort.SliceStable(c.list, func(i, j int) bool {
		a, b := strings.ToLower(c.list[i].Name), strings.ToLower(c.list[j].Name)
		if a != b {
			return a < b
		}
		return c.list[i].ID < c.list[j].ID
	})
	return c, nil
}

var yearRe = regexp.MustCompile(`^(19|20)[0-9][0-9]$`)

// slugOf turns a short name into a page address ("NYC Marathon - Blue
// Route" -> "nyc-marathon-blue-route"): lower case, letters and digits only,
// runs of anything else as one "-".
func slugOf(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	return b.String()
}

// Standard distances written into some race names, and the race type
// each stands for.
var (
	marathonRe = regexp.MustCompile(`(?i)\b(42\.195?|42\.2|26\.2)\s*(km|k|kilometers?|kilometres?|mi|miles?)?\b`)
	halfRe     = regexp.MustCompile(`(?i)\b(21\.0975|21\.1|13\.1)\s*(km|k|kilometers?|kilometres?|mi|miles?)?\b`)
	typeRe     = regexp.MustCompile(`(?i)marathon`)
	// "Foo - Marathon", left by "Foo - 26.2 mi", reads "Foo Marathon".
	kindAfterDashRe = regexp.MustCompile(`\s+[-–]\s+((Half )?Marathon)$`)
	dashesRe        = regexp.MustCompile(`(\s+[-–]\s*)+$|^(\s*[-–]\s+)+|\s+[-–]\s+[-–]\s+`)
)

// shortNameOf is a race's name for page titles and links: without its
// years, and with a standard distance ("42.195 km", "13.1 mi") swapped for
// the race type it stands for ("NYC Marathon 2026 - Blue Route" -> "NYC
// Marathon - Blue Route", "TCS Sydney Marathon 2025 - 42.195 km" -> "TCS
// Sydney Marathon", "Foo City 13.1" -> "Foo City Half Marathon").
func shortNameOf(name string) string {
	var keep []string
	for _, w := range strings.Fields(name) {
		if !yearRe.MatchString(w) {
			keep = append(keep, w)
		}
	}
	short := strings.Join(keep, " ")
	for _, d := range []struct {
		re   *regexp.Regexp
		kind string
	}{{halfRe, "Half Marathon"}, {marathonRe, "Marathon"}} {
		if !d.re.MatchString(short) {
			continue
		}
		// The type goes where the distance was, unless the name has it.
		with := ""
		if !typeRe.MatchString(d.re.ReplaceAllString(short, "")) {
			with = d.kind
		}
		short = d.re.ReplaceAllLiteralString(short, with)
	}
	short = strings.Join(strings.Fields(short), " ")
	short = strings.Trim(dashesRe.ReplaceAllString(short, " - "), " -–")
	short = kindAfterDashRe.ReplaceAllString(short, " $1")
	if short == "" {
		return name
	}
	return short
}

// assignSlugs gives each race its slug, the latest id first so the newest
// year of a race wins a shared slug.
func assignSlugs(list []Race) {
	order := make([]int, len(list))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return list[order[a]].ID > list[order[b]].ID })
	taken := map[string]bool{}
	for _, i := range order {
		slug := slugOf(list[i].ShortName)
		if slug == "" || taken[slug] {
			slug = list[i].ID
		}
		taken[slug] = true
		list[i].Slug = slug
	}
}

// List returns the races sorted by name (case-insensitive). It is never nil.
func (c *Catalog) List() []Race {
	return append([]Race{}, c.list...)
}

// GPX returns the raw file for id. Only ids in the catalog match, so path
// tricks like "../x" simply miss.
func (c *Catalog) GPX(id string) ([]byte, bool) {
	b, ok := c.gpx[id]
	return b, ok
}

// Default is the catalog of the races embedded in the binary.
var Default = mustDefault()

func mustDefault() *Catalog {
	sub, err := fs.Sub(embedded, "data")
	if err != nil {
		panic(err)
	}
	c, err := NewCatalog(sub)
	if err != nil {
		panic(err)
	}
	return c
}
