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
	"sort"
	"strings"

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
		c.list = append(c.list, Race{ID: id, Name: name})
		c.gpx[id] = data
	}
	sort.SliceStable(c.list, func(i, j int) bool {
		a, b := strings.ToLower(c.list[i].Name), strings.ToLower(c.list[j].Name)
		if a != b {
			return a < b
		}
		return c.list[i].ID < c.list[j].ID
	})
	return c, nil
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
