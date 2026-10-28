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
	"strconv"
	"strings"
	"time"
)

// openFeed returns a file system rooted at the feed's .txt files. feedPath
// is a .zip or a directory. A zip whose files sit in a single top-level
// folder is accepted too.
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
	if _, err := fs.Stat(fsys, "stops.txt"); err == nil {
		return fsys, closer, nil
	}
	matches, _ := fs.Glob(fsys, "*/stops.txt")
	if len(matches) == 1 {
		sub, err := fs.Sub(fsys, path.Dir(matches[0]))
		if err == nil {
			return sub, closer, nil
		}
	}
	closer()
	return nil, nil, errors.New("no stops.txt found")
}

// table reads one GTFS CSV file by column name.
type table struct {
	name string
	f    io.Closer
	r    *csv.Reader
	cols map[string]int
	rec  []string
}

// openTable opens name in fsys. A missing optional file returns (nil, nil);
// methods on a nil table behave like an empty file.
func openTable(fsys fs.FS, name string, required bool) (*table, error) {
	f, err := fsys.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !required {
			return nil, nil
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	br := bufio.NewReaderSize(f, 1<<16)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		br.Discard(3)
	}
	r := csv.NewReader(br)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = true
	t := &table{name: name, f: f, r: r, cols: map[string]int{}}
	hdr, err := r.Read()
	if err == io.EOF {
		return t, nil
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	for i, h := range hdr {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))
		if _, dup := t.cols[h]; !dup {
			t.cols[h] = i
		}
	}
	return t, nil
}

// next advances to the next record. It returns false at the end of the
// file or on error; check err.
func (t *table) next() (bool, error) {
	if t == nil || len(t.cols) == 0 {
		return false, nil
	}
	rec, err := t.r.Read()
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%s: %w", t.name, err)
	}
	t.rec = rec
	return true, nil
}

func (t *table) has(col string) bool {
	if t == nil {
		return false
	}
	_, ok := t.cols[col]
	return ok
}

// get returns the trimmed value of col in the current record, or "" if the
// column or the field is missing.
func (t *table) get(col string) string {
	i, ok := t.cols[col]
	if !ok || i >= len(t.rec) {
		return ""
	}
	return strings.TrimSpace(t.rec[i])
}

func (t *table) close() {
	if t != nil {
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
