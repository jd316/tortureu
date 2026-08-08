package capture

import (
	"bytes"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// Result summarizes a replay run — the same shape `smoke` reports (requests
// sent, success count, latency percentiles), because replay is load the
// same way `smoke` is load; it just gets its requests from a cassette
// instead of a flat rate.
type Result struct {
	Sent    int
	Success int
	Failed  int
	P50MS   float64
	P95MS   float64
	P99MS   float64
}

// Replay drives every Entry in entries against target, repeat times each,
// in cassette order. It does not consult egress.CheckMultiplier itself —
// the R-DC2-4 guard is the caller's responsibility (cmd/tortureu/replay.go)
// because only the caller knows the host's egress class; Replay only knows
// how to send HTTP requests.
//
// repeat is the caller-resolved request-count multiplier (already rounded
// from -multiplier), not a rate: v0 replays as fast as target answers,
// sequentially. There is no scheduling model here to honour cassette
// inter-request timing — RESEARCH.md's premise is "replay it as load", and
// a v0 sequential replay already is load; pacing is left as a documented
// gap, not implemented as a guess.
func Replay(entries []Entry, target *url.URL, repeat int, client *http.Client) (Result, error) {
	if repeat < 1 {
		repeat = 1
	}
	if client == nil {
		client = http.DefaultClient
	}

	var res Result
	var latencies []float64

	for _, e := range entries {
		body, err := e.RequestBody()
		if err != nil {
			return res, fmt.Errorf("capture: replay: decode entry %d body: %w", e.Seq, err)
		}
		for i := 0; i < repeat; i++ {
			start := time.Now()
			ok := doReplayRequest(client, target, e, body)
			elapsed := time.Since(start)

			res.Sent++
			if ok {
				res.Success++
			} else {
				res.Failed++
			}
			latencies = append(latencies, float64(elapsed.Microseconds())/1000.0)
		}
	}

	res.P50MS = percentile(latencies, 0.50)
	res.P95MS = percentile(latencies, 0.95)
	res.P99MS = percentile(latencies, 0.99)
	return res, nil
}

func doReplayRequest(client *http.Client, target *url.URL, e Entry, body []byte) bool {
	dest := *target
	// e.URL is the original request's path+query (already scrubbed at
	// capture time); replay always targets the -target host, never
	// whatever host happened to be embedded in the cassette.
	if u, err := url.Parse(e.URL); err == nil {
		dest.Path = u.Path
		dest.RawQuery = u.RawQuery
	}
	req, err := http.NewRequest(e.Method, dest.String(), bytes.NewReader(body))
	if err != nil {
		return false
	}
	for k, vs := range e.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}

// percentile returns the p-th percentile (0..1) of samples using
// nearest-rank, sorting a copy so the caller's slice order is preserved.
func percentile(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	cp := append([]float64(nil), samples...)
	sort.Float64s(cp)
	idx := int(math.Ceil(p*float64(len(cp)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(cp) {
		idx = len(cp) - 1
	}
	return cp[idx]
}
