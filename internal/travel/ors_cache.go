package travel

import (
	"bufio"
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/logie4000/cheersquad/internal/geo"
)

// CacheMaxAge is how long a saved ORS answer is trusted. Streets change
// slowly, so this is long; older answers are dropped when the cache loads.
var CacheMaxAge = 180 * 24 * time.Hour

// cacheLine is one saved ORS answer, one JSON object per line. Kind "d"
// is a directions route, "m" a matrix duration; No means ORS found no
// route.
type cacheLine struct {
	Kind    string       `json:"k"`
	Mode    Mode         `json:"m"`
	NoFerry bool         `json:"nf,omitempty"`
	A       [2]float64   `json:"a"`
	B       [2]float64   `json:"b"`
	Seconds float64      `json:"s,omitempty"`
	Meters  float64      `json:"d,omitempty"`
	Path    [][2]float64 `json:"p,omitempty"`
	No      bool         `json:"no,omitempty"`
	Day     int64        `json:"t"` // days since the Unix epoch, UTC
}

// orsCache appends answers to a file so they outlive the container. The
// preloaded courses' legs are asked for again and again, and each saved
// answer is one request less against the daily quota.
type orsCache struct {
	path string
	f    *os.File
	w    *bufio.Writer
	err  bool // writing failed once; stop trying
}

// OpenCache loads saved answers from path (which need not exist yet) and
// appends new ones to it from now on. Answers older than CacheMaxAge are
// dropped, and the file is rewritten when it holds many dropped or
// repeated lines. Call it before serving requests.
func (o *ORS) OpenCache(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lines, err := o.loadCache(path)
	if err != nil {
		return err
	}
	o.mu.Lock()
	kept := len(o.memo) + len(o.times)
	o.mu.Unlock()
	if lines > kept+kept/4+100 {
		if err := o.rewriteCache(path); err != nil {
			log.Printf("ors cache: could not compact %s: %v", path, err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	o.mu.Lock()
	o.cache = &orsCache{path: path, f: f, w: bufio.NewWriterSize(f, 64<<10)}
	nd, nm := len(o.memo), len(o.times)
	o.mu.Unlock()
	log.Printf("ors cache: %d routes and %d matrix times from %s", nd, nm, path)
	return nil
}

// Close flushes and closes the cache file, if one is open.
func (o *ORS) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	c := o.cache
	if c == nil {
		return nil
	}
	o.cache = nil
	err := c.w.Flush()
	if cerr := c.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// loadCache reads path into the memo and times, and returns how many
// lines it read.
func (o *ORS) loadCache(path string) (int, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	oldest := today() - int64(CacheMaxAge/(24*time.Hour))
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.memo == nil {
		o.memo = map[orsKey]orsVal{}
	}
	if o.times == nil {
		o.times = map[orsKey]orsTime{}
	}
	lines := 0
	for sc.Scan() {
		lines++
		var l cacheLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Day < oldest {
			continue // a torn last line, or too old
		}
		k := orsKey{latLon(l.A), latLon(l.B), l.Mode, l.NoFerry}
		switch l.Kind {
		case "d":
			v := orsVal{ok: !l.No, day: l.Day}
			if v.ok {
				v.r = StreetRoute{Seconds: l.Seconds, Meters: l.Meters, Path: make([]geo.LatLon, len(l.Path))}
				for i, p := range l.Path {
					v.r.Path[i] = latLon(p)
				}
			}
			o.memo[k] = v
		case "m":
			t := orsTime{l.Seconds, l.Day}
			if l.No {
				t.s = math.NaN()
			}
			k.noFerry = false
			o.times[k] = t
		}
	}
	o.trimLocked()
	return lines, sc.Err()
}

// rewriteCache writes the memo and times to path, replacing it.
func (o *ORS) rewriteCache(path string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ors-cache-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriterSize(tmp, 64<<10)
	enc := json.NewEncoder(w)
	o.mu.Lock()
	for k, v := range o.memo {
		if err == nil {
			err = enc.Encode(routeLine(k, v))
		}
	}
	for k, t := range o.times {
		if err == nil {
			err = enc.Encode(timeLine(k, t))
		}
	}
	o.mu.Unlock()
	if err == nil {
		err = w.Flush()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// saveLocked appends lines to the cache file, if one is open. The caller
// holds o.mu.
func (o *ORS) saveLocked(ls ...cacheLine) {
	c := o.cache
	if c == nil || c.err || len(ls) == 0 {
		return
	}
	enc := json.NewEncoder(c.w)
	var err error
	for _, l := range ls {
		if err = enc.Encode(l); err != nil {
			break
		}
	}
	if err == nil {
		err = c.w.Flush()
	}
	if err != nil {
		c.err = true
		log.Printf("ors cache: writing %s failed, answers are kept in memory only: %v", c.path, err)
	}
}

// trimLocked forgets the oldest tenth of the memo or times when either
// is over its cap. The caller holds o.mu.
func (o *ORS) trimLocked() {
	maxMemo, maxTimes := o.MaxMemo, o.MaxTimes
	if maxMemo <= 0 {
		maxMemo = 10000
	}
	if maxTimes <= 0 {
		maxTimes = 300000
	}
	if len(o.memo) > maxMemo {
		drop := oldestKeys(o.memo, func(v orsVal) int64 { return v.day }, len(o.memo)-maxMemo*9/10)
		for _, k := range drop {
			delete(o.memo, k)
		}
	}
	if len(o.times) > maxTimes {
		drop := oldestKeys(o.times, func(t orsTime) int64 { return t.day }, len(o.times)-maxTimes*9/10)
		for _, k := range drop {
			delete(o.times, k)
		}
	}
}

// oldestKeys returns n keys of m with the smallest days.
func oldestKeys[V any](m map[orsKey]V, day func(V) int64, n int) []orsKey {
	type kd struct {
		k orsKey
		d int64
	}
	all := make([]kd, 0, len(m))
	for k, v := range m {
		all = append(all, kd{k, day(v)})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].d < all[j].d })
	out := make([]orsKey, 0, n)
	for _, e := range all[:min(n, len(all))] {
		out = append(out, e.k)
	}
	return out
}

func routeLine(k orsKey, v orsVal) cacheLine {
	l := cacheLine{Kind: "d", Mode: k.m, NoFerry: k.noFerry, A: pair(k.a), B: pair(k.b), Day: v.day}
	if !v.ok {
		l.No = true
		return l
	}
	l.Seconds, l.Meters = v.r.Seconds, v.r.Meters
	l.Path = make([][2]float64, len(v.r.Path))
	for i, p := range v.r.Path {
		l.Path[i] = pair(p)
	}
	return l
}

func timeLine(k orsKey, t orsTime) cacheLine {
	l := cacheLine{Kind: "m", Mode: k.m, A: pair(k.a), B: pair(k.b), Day: t.day}
	if math.IsNaN(t.s) {
		l.No = true
	} else {
		l.Seconds = t.s
	}
	return l
}

func pair(p geo.LatLon) [2]float64   { return [2]float64{p.Lat, p.Lon} }
func latLon(p [2]float64) geo.LatLon { return geo.LatLon{Lat: p[0], Lon: p[1]} }
func today() int64                   { return time.Now().Unix() / 86400 }
