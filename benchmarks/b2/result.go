package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// stats is one configuration's round-trip latency summary plus the
// throughput actually sustained over the real (not requested) wall-clock
// window.
type stats struct {
	N       int     `json:"n"`
	Mean    float64 `json:"mean_ms"`
	P50     float64 `json:"p50_ms"`
	P95     float64 `json:"p95_ms"`
	P99     float64 `json:"p99_ms"`
	RPS     float64 `json:"requests_per_sec"`
	Workers int     `json:"workers"`
}

func computeStats(latenciesMs []float64, wallSeconds float64, workers int) stats {
	n := len(latenciesMs)
	if n == 0 {
		return stats{Workers: workers}
	}
	sorted := append([]float64(nil), latenciesMs...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	pct := func(p float64) float64 {
		idx := int(math.Ceil(p*float64(n))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= n {
			idx = n - 1
		}
		return sorted[idx]
	}
	rps := 0.0
	if wallSeconds > 0 {
		rps = float64(n) / wallSeconds
	}
	return stats{
		N: n, Mean: sum / float64(n),
		P50: pct(0.50), P95: pct(0.95), P99: pct(0.99),
		RPS: rps, Workers: workers,
	}
}

// loadResult is loadClientPy's parsed JSON output for one run.
type loadResult struct {
	LatenciesMs  []float64
	N            int
	WallSeconds  float64
	WorkerErrors int
}

// configResult is one of the three B2 configurations' published row:
// requested scenario, measured stats, and (direct's absence aside) the
// delta against the direct baseline that is the actual point of B2.
type configResult struct {
	Config       string   `json:"config"`
	Verdict      string   `json:"verdict"` // "measured" | "unmeasured"
	Stats        *stats   `json:"stats,omitempty"`
	DeltaP50Ms   float64  `json:"delta_p50_ms,omitempty"`
	DeltaP95Ms   float64  `json:"delta_p95_ms,omitempty"`
	DeltaP99Ms   float64  `json:"delta_p99_ms,omitempty"`
	DeltaRPSPct  float64  `json:"delta_rps_pct,omitempty"`
	Notes        []string `json:"notes,omitempty"`
	WorkerErrors int      `json:"worker_errors,omitempty"`
}

func (c *configResult) note(format string, args ...any) {
	c.Notes = append(c.Notes, fmt.Sprintf(format, args...))
}

// generatorCeilingInfo is BENCHMARKS.md §B2's required companion to any rps
// number: the generator's own limits on this machine, read from the actual
// container generating load.
type generatorCeilingInfo struct {
	FDLimit            string `json:"fd_limit"`
	EphemeralPortRange string `json:"ephemeral_port_range"`
	CPUModel           string `json:"cpu_model"`
	CPUCores           int    `json:"cpu_cores"`
}

// result is a helper return type withStack's callback uses to report a
// harness-level (as opposed to measurement-level) failure, mirroring b1's
// Result.Verdict=="unmeasured" convention.
type result struct {
	unmeasuredNote string
}

func lastJSONObject(s string) (map[string]any, error) {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err == nil {
			return obj, nil
		}
	}
	return nil, fmt.Errorf("no JSON object found in output: %q", s)
}

func float64s(v any) []float64 {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]float64, 0, len(arr))
	for _, x := range arr {
		if f, ok := x.(float64); ok {
			out = append(out, f)
		}
	}
	return out
}
