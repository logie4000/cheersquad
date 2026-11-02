package gtfs

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// openFeed returns a file system rooted at the feed's .txt files. feedPath
// is a .zip or a directory. A zip whose files sit in a single top-level
// folder is accepted too. A zip or directory holding several feed zips
// instead (a city whose agencies publish separately, such as BART and
// Muni, or SEPTA's bus and rail bundle) is read as one merged feed.
func openFeed(feedPath string) (fs.FS, func() error, error) {
	st, err := os.Stat(feedPath)
	if err != nil {
		return nil, nil, err
	}
	var fsys fs.FS
	closer := func() error { return nil }
	if st.IsDir() {
		fsys = os.DirFS(feedPath)
	} else {
		zr, err := zip.OpenReader(feedPath)
		if err != nil {
			return nil, nil, err
		}
		fsys, closer = zr, zr.Close
	}
	if root, ok := feedRoot(fsys); ok {
		return root, closer, nil
	}
	m, closeParts, err := openParts(fsys, feedPath, st.IsDir())
	if err != nil || m == nil {
		closer()
		if err == nil {
			err = errors.New("no stops.txt found")
		}
		return nil, nil, err
	}
	closeAll := func() error {
		closeParts()
		return closer()
	}
	if len(m.parts) == 1 {
		return m.parts[0], closeAll, nil
	}
	return m, closeAll, nil
}

// feedRoot returns the folder of fsys holding stops.txt: the top, or a
// single top-level folder.
func feedRoot(fsys fs.FS) (fs.FS, bool) {
	if _, err := fs.Stat(fsys, "stops.txt"); err == nil {
		return fsys, true
	}
	matches, _ := fs.Glob(fsys, "*/stops.txt")
	if len(matches) == 1 {
		if sub, err := fs.Sub(fsys, path.Dir(matches[0])); err == nil {
			return sub, true
		}
	}
	return nil, false
}

// openParts opens the feed zips at the top of fsys, in name order, or
// returns nil if there are none. Zips inside a zip are read into memory.
func openParts(fsys fs.FS, feedPath string, isDir bool) (*mergedFeed, func(), error) {
	entries, _ := fs.ReadDir(fsys, ".")
	m := &mergedFeed{}
	var closers []func() error
	closeAll := func() {
		for _, c := range closers {
			c()
		}
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.EqualFold(path.Ext(name), ".zip") || strings.HasPrefix(name, ".") {
			continue
		}
		var zr *zip.Reader
		if isDir {
			rc, err := zip.OpenReader(filepath.Join(feedPath, name))
			if err != nil {
				closeAll()
				return nil, nil, fmt.Errorf("%s: %w", name, err)
			}
			closers = append(closers, rc.Close)
			zr = &rc.Reader
		} else {
			b, err := fs.ReadFile(fsys, name)
			if err == nil {
				zr, err = zip.NewReader(bytes.NewReader(b), int64(len(b)))
			}
			if err != nil {
				closeAll()
				return nil, nil, fmt.Errorf("%s: %w", name, err)
			}
		}
		root, ok := feedRoot(zr)
		if !ok {
			closeAll()
			return nil, nil, fmt.Errorf("%s: no stops.txt found", name)
		}
		m.names = append(m.names, name[:len(name)-len(path.Ext(name))])
		m.parts = append(m.parts, root)
	}
	if len(m.parts) == 0 {
		return nil, closeAll, nil
	}
	return m, closeAll, nil
}

// mergedFeed is several GTFS feeds read as one. Tables are read feed after
// feed, with each feed's ids prefixed by its name so they can't collide;
// stations of different feeds close together become walking transfers like
// any others.
type mergedFeed struct {
	names []string
	parts []fs.FS
}

// Open makes mergedFeed an fs.FS; its tables are read through openTable.
func (m *mergedFeed) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// idCols are the columns holding ids, prefixed in a merged feed.
var idCols = map[string]bool{
	"agency_id": true, "route_id": true, "trip_id": true, "service_id": true, "shape_id": true,
	"stop_id": true, "parent_station": true, "from_stop_id": true, "to_stop_id": true,
}

// table reads one GTFS CSV file by column name.
type table struct {
	name string
	f    io.Closer
	r    *csv.Reader
	cols map[string]int
	rec  []string

	// For a merged feed: the feed and the index of the part being read,
	// whose ids get prefix.
	feed   *mergedFeed
	part   int
	prefix string
}

// openTable opens name in fsys. A missing optional file returns (nil, nil);
// methods on a nil table behave like an empty file.
func openTable(fsys fs.FS, name string, required bool) (*table, error) {
	m, ok := fsys.(*mergedFeed)
	if !ok {
		t := &table{name: name}
		if err := t.open(fsys); err != nil {
			if errors.Is(err, fs.ErrNotExist) && !required {
				return nil, nil
			}
			return nil, err
		}
		return t, nil
	}
	t := &table{name: name, feed: m, part: -1}
	found, err := t.nextPart()
	if err != nil {
		return nil, err
	}
	if !found {
		if required {
			return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
		}
		return nil, nil
	}
	return t, nil
}

// open starts reading t.name in fsys.
func (t *table) open(fsys fs.FS) error {
	f, err := fsys.Open(t.name)
	if err != nil {
		return fmt.Errorf("%s: %w", t.name, err)
	}
	br := bufio.NewReaderSize(f, 1<<16)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		br.Discard(3)
	}
	r := csv.NewReader(br)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = true
	t.f, t.r, t.cols, t.rec = f, r, map[string]int{}, nil
	hdr, err := r.Read()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", t.name, err)
	}
	for i, h := range hdr {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))
		if _, dup := t.cols[h]; !dup {
			t.cols[h] = i
		}
	}
	return nil
}

// nextPart moves a merged table on to the next feed that has the file,
// returning false when there is none.
func (t *table) nextPart() (bool, error) {
	for t.part+1 < len(t.feed.parts) {
		t.part++
		t.close()
		t.f, t.cols = nil, nil
		err := t.open(t.feed.parts[t.part])
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("%s: %w", t.feed.names[t.part], err)
		}
		t.prefix = t.feed.names[t.part] + ":"
		return true, nil
	}
	return false, nil
}

// next advances to the next record. It returns false at the end of the
// file (of the last feed's file, in a merged feed) or on error; check err.
func (t *table) next() (bool, error) {
	if t == nil {
		return false, nil
	}
	for {
		if len(t.cols) > 0 {
			rec, err := t.r.Read()
			if err == nil {
				t.rec = rec
				return true, nil
			}
			if err != io.EOF {
				return false, fmt.Errorf("%s: %w", t.name, err)
			}
		}
		if t.feed == nil {
			return false, nil
		}
		if ok, err := t.nextPart(); !ok || err != nil {
			return false, err
		}
	}
}

func (t *table) has(col string) bool {
	if t == nil {
		return false
	}
	_, ok := t.cols[col]
	return ok
}

// get returns the trimmed value of col in the current record, or "" if the
// column or the field is missing. In a merged feed, ids come with their
// feed's prefix; agency_id always does, as a feed with one agency may leave
// it out.
func (t *table) get(col string) string {
	v := ""
	if i, ok := t.cols[col]; ok && i < len(t.rec) {
		v = strings.TrimSpace(t.rec[i])
	}
	if t.prefix != "" && idCols[col] && (v != "" || col == "agency_id") {
		v = t.prefix + v
	}
	return v
}

func (t *table) close() {
	if t != nil && t.f != nil {
		t.f.Close()
	}
}

// requireCols reports an error naming the first missing required column.
func (t *table) requireCols(cols ...string) error {
	if t == nil {
		return nil
	}
	for _, c := range cols {
		if !t.has(c) {
			return fmt.Errorf("%s: missing column %s", t.name, c)
		}
	}
	return nil
}

// parseTime parses a GTFS time H:MM:SS, where hours may be 24 or more for
// trips running past midnight of the service day. It returns seconds since
// the start of the service day, and false for an empty or bad value.
func parseTime(s string) (int32, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || h < 0 || m < 0 || m > 59 || sec < 0 || sec > 59 {
		return 0, false
	}
	return int32(h*3600 + m*60 + sec), true
}

func parseDate(s string) (time.Time, bool) {
	t, err := time.Parse("20060102", strings.TrimSpace(s))
	return t, err == nil
}
