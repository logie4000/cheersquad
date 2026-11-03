package travel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/logie4000/cheersquad/internal/geo"
)

// Pair is a leg from A to B.
type Pair struct{ A, B geo.LatLon }

// PairTimer times many legs in a few requests, without paths. Seconds are
// the router's normal-day times; NaN means no route.
type PairTimer interface {
	Times(ctx context.Context, pairs []Pair, m Mode) (map[Pair]float64, error)
}

// ErrTooManyPairs means the legs would take more requests than allowed.
var ErrTooManyPairs = errors.New("too many legs to time")

// MatrixCells is the most sources x destinations in one ORS matrix
// request on the free plan.
const MatrixCells = 3500

// MaxMatrixRequests caps the matrix requests for one call to Times.
const MaxMatrixRequests = 6

// matrixBlock is one request: every source to every destination.
type matrixBlock struct{ src, dst []geo.LatLon }

// blocks covers the pairs with as few requests as it can manage simply:
// sources in the order given, each block growing until another source
// would pass MatrixCells. A source with more destinations than fit is
// split across blocks of its own.
func blocks(pairs []Pair) []matrixBlock {
	var order []geo.LatLon
	dests := map[geo.LatLon][]geo.LatLon{}
	for _, p := range pairs {
		if _, ok := dests[p.A]; !ok {
			order = append(order, p.A)
		}
		dests[p.A] = append(dests[p.A], p.B)
	}
	var out []matrixBlock
	var cur matrixBlock
	inCur := map[geo.LatLon]bool{}
	flush := func() {
		if len(cur.src) > 0 {
			out = append(out, cur)
		}
		cur, inCur = matrixBlock{}, map[geo.LatLon]bool{}
	}
	for _, a := range order {
		var add []geo.LatLon
		for _, b := range uniq(dests[a]) {
			if !inCur[b] {
				add = append(add, b)
			}
		}
		if (len(cur.src)+1)*(len(cur.dst)+len(add)) > MatrixCells {
			flush()
			add = uniq(dests[a])
		}
		if len(add) > MatrixCells {
			for i := 0; i < len(add); i += MatrixCells {
				out = append(out, matrixBlock{src: []geo.LatLon{a}, dst: add[i:min(i+MatrixCells, len(add))]})
			}
			continue
		}
		cur.src = append(cur.src, a)
		for _, b := range add {
			inCur[b] = true
			cur.dst = append(cur.dst, b)
		}
	}
	flush()
	return out
}

func uniq(ps []geo.LatLon) []geo.LatLon {
	seen := map[geo.LatLon]bool{}
	var out []geo.LatLon
	for _, p := range ps {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// Times asks the ORS matrix API for the pairs not already remembered, in
// at most MaxMatrixRequests requests (ErrTooManyPairs if they would need
// more). The matrix API cannot avoid ferries, so callers that must should
// not use it. On a failure part way it returns what it has with the error.
func (o *ORS) Times(ctx context.Context, pairs []Pair, m Mode) (map[Pair]float64, error) {
	profile, ok := orsProfiles[m]
	if !ok {
		return nil, fmt.Errorf("ors: no profile for %s", m)
	}
	out := map[Pair]float64{}
	var missing []Pair
	o.mu.Lock()
	for _, p := range pairs {
		if t, hit := o.times[orsKey{p.A, p.B, m, false}]; hit {
			out[p] = t.s
		} else {
			missing = append(missing, p)
		}
	}
	o.mu.Unlock()
	bs := blocks(missing)
	if len(bs) > MaxMatrixRequests {
		return out, ErrTooManyPairs
	}
	for _, b := range bs {
		var rows [][]float64
		var err error
		for attempt := 0; ; attempt++ {
			if err = o.take(ctx, &o.mat); err != nil {
				return out, err
			}
			rows, err = o.matrix(ctx, profile, b)
			var se *orsStatusError
			if attempt == 0 && errors.As(err, &se) && se.code == http.StatusTooManyRequests {
				o.backOff(&o.mat)
				continue
			}
			break
		}
		if err != nil {
			if ctx.Err() == nil {
				o.fail(&o.mat, err)
			}
			return out, ErrRouterUnavailable
		}
		day := today()
		o.mu.Lock()
		if o.times == nil {
			o.times = map[orsKey]orsTime{}
		}
		var lines []cacheLine
		for i, a := range b.src {
			for j, d := range b.dst {
				k, t := orsKey{a, d, m, false}, orsTime{rows[i][j], day}
				o.times[k] = t
				out[Pair{a, d}] = t.s
				lines = append(lines, timeLine(k, t))
			}
		}
		o.trimLocked()
		o.saveLocked(lines...)
		o.mu.Unlock()
	}
	return out, nil
}

type orsMatrixRequest struct {
	Locations    [][2]float64 `json:"locations"`
	Sources      []int        `json:"sources"`
	Destinations []int        `json:"destinations"`
	Metrics      []string     `json:"metrics"`
}

// matrix fetches durations from every source to every destination of b;
// rows[i][j] is NaN when there is no route.
func (o *ORS) matrix(ctx context.Context, profile string, b matrixBlock) ([][]float64, error) {
	req := orsMatrixRequest{Metrics: []string{"duration"}}
	for i, p := range b.src {
		req.Locations = append(req.Locations, [2]float64{p.Lon, p.Lat})
		req.Sources = append(req.Sources, i)
	}
	for i, p := range b.dst {
		req.Locations = append(req.Locations, [2]float64{p.Lon, p.Lat})
		req.Destinations = append(req.Destinations, len(b.src)+i)
	}
	buf, _ := json.Marshal(req)
	base := o.BaseURL
	if base == "" {
		base = "https://api.openrouteservice.org"
	}
	u := strings.TrimRight(base, "/") + "/v2/matrix/" + profile
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Authorization", o.Key)
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	resp, err := o.client().Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var r struct {
		Durations [][]*float64 `json:"durations"`
		Error     *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	jsonErr := json.Unmarshal(raw, &r)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if r.Error != nil {
			msg = fmt.Sprintf("code %d: %s", r.Error.Code, r.Error.Message)
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, &orsStatusError{resp.StatusCode, msg}
	}
	if jsonErr != nil {
		return nil, jsonErr
	}
	if len(r.Durations) != len(b.src) {
		return nil, fmt.Errorf("matrix: %d rows for %d sources", len(r.Durations), len(b.src))
	}
	rows := make([][]float64, len(b.src))
	for i, row := range r.Durations {
		if len(row) != len(b.dst) {
			return nil, fmt.Errorf("matrix: row of %d for %d destinations", len(row), len(b.dst))
		}
		rows[i] = make([]float64, len(row))
		for j, d := range row {
			rows[i][j] = math.NaN()
			if d != nil {
				rows[i][j] = *d
			}
		}
	}
	return rows, nil
}
