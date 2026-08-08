package main

import (
	"fmt"
	"math"
	"sort"
)

// Stats is the raw sample summary published alongside every measured verb
// (rule 2/3: never publish a bare pass/fail without the numbers behind it).
type Stats struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	Stddev float64 `json:"stddev"`
	P50    float64 `json:"p50"`
}

func computeStats(samples []float64) Stats {
	n := len(samples)
	if n == 0 {
		return Stats{}
	}
	sum := 0.0
	for _, v := range samples {
		sum += v
	}
	mean := sum / float64(n)
	var variance float64
	for _, v := range samples {
		d := v - mean
		variance += d * d
	}
	variance /= float64(n)
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	p50 := sorted[n/2]
	if n%2 == 0 && n >= 2 {
		p50 = (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return Stats{N: n, Mean: mean, Stddev: math.Sqrt(variance), P50: p50}
}

// Result is one verb's published row: what was requested, what the harness
// actually observed driving it through the real fault path, the tolerance it
// was judged against, and the verdict — "pass", "miss", or "unmeasured"
// (rule 3: never "passed" for something not actually measured).
type Result struct {
	Verb       string   `json:"verb"`
	Requested  string   `json:"requested"`
	Measured   any      `json:"measured,omitempty"`
	Tolerance  string   `json:"tolerance"`
	Verdict    string   `json:"verdict"` // pass | miss | unmeasured
	Stats      *Stats   `json:"stats,omitempty"`
	Translated string   `json:"translated_action,omitempty"`
	Notes      []string `json:"notes,omitempty"`
	Secondary  *Result  `json:"secondary_numeric_measurement,omitempty"`
}

func (r *Result) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}
