// Package smoke implements `tortureu smoke` (R-CLI-6, proposed): a
// constant-rate sanity check that needs no torture.yaml. It answers one
// question — "is this stack up, and does it survive trivial load?" — and
// reports one number and an exit code. It deliberately does not do what
// `tortureu run` does: no fault injection, no egress classification, no
// verdict document (R-VER-9's "one document, one renderer" reasoning
// applies in reverse here — smoke is not a second, competing source of
// verdicts, so it never produces anything that looks like one).
//
// registry.yaml names vegeta as the drive-tier tool for this ("constant-rate
// sanity check, no script"). This package does not shell out to the vegeta
// binary: driving it would mean either (a) letting it dial the SUT
// directly, which reintroduces exactly the DC-2 reachability bug
// internal/run's K6Runner/inreach.go already had to fix once (an
// internal-only SUT network publishes no host port a host-process vegeta
// could dial), or (b) standing up a local HTTP proxy just so vegeta's
// traffic can be routed through the same container-network-namespace
// tunnel this package needs anyway — machinery disproportionate to "one
// number and an exit code". Go's own net/http already gives a constant-rate
// loop a custom Dialer for free, so that is what Run below does: a small
// in-process constant-rate HTTP loop, driven at a fixed tick rate, is
// vegeta's constant-rate model without the extra process.
package smoke

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Options configures one smoke run. Rate and Duration default to low,
// sanity-check values in the CLI wiring (cmd/tortureu/smoke.go) — this is
// meant to answer "is it alive", not to load-test.
type Options struct {
	Rate     float64       // requests per second
	Duration time.Duration // total wall-clock time to drive traffic
	Timeout  time.Duration // per-request timeout
}

// Result is what came back: counts and latency percentiles. Nothing here
// is a verdict — no status, no confidence, no attribution (that vocabulary
// belongs to internal/verdict and only to `run`).
type Result struct {
	Sent, OK, Failed int
	P50, P95, P99    time.Duration
}

// SuccessRate is OK/Sent, or 0 when nothing was sent.
func (r Result) SuccessRate() float64 {
	if r.Sent == 0 {
		return 0
	}
	return float64(r.OK) / float64(r.Sent)
}

// Run drives url at a constant rate (Options.Rate requests/second) for
// Options.Duration and reports what came back. A response is "OK" when its
// status code is 2xx or 3xx; anything else — including a transport-level
// error (connection refused, timeout) — counts as Failed, never silently
// dropped from Sent.
//
// Each tick fires a request in its own goroutine so a slow or hanging
// response never delays the next tick (the point of a *constant-rate*
// driver — vegeta's own model, which is why registry.yaml names it). After
// the ticker stops, Run waits for in-flight requests to finish, bounded by
// Options.Timeout, so a dead server cannot make Run hang past its declared
// budget.
func Run(client *http.Client, url string, opts Options) Result {
	interval := time.Duration(float64(time.Second) / opts.Rate)
	if interval <= 0 {
		interval = time.Second
	}

	var mu sync.Mutex
	var latencies []time.Duration
	var sent, ok int

	var wg sync.WaitGroup
	fire := func() {
		mu.Lock()
		sent++
		mu.Unlock()

		start := time.Now()
		resp, err := client.Get(url)
		elapsed := time.Since(start)
		success := err == nil && resp.StatusCode >= 200 && resp.StatusCode < 400
		if resp != nil {
			resp.Body.Close()
		}

		mu.Lock()
		latencies = append(latencies, elapsed)
		if success {
			ok++
		}
		mu.Unlock()
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	deadline := time.Now().Add(opts.Duration)
	for time.Now().Before(deadline) {
		<-ticker.C
		wg.Add(1)
		go func() {
			defer wg.Done()
			fire()
		}()
	}

	// Bound the wait for stragglers: every request already carries
	// client.Timeout (wired by the caller), so this is a backstop, not the
	// primary timeout enforcement.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(opts.Timeout + time.Second):
	}

	mu.Lock()
	defer mu.Unlock()
	return Result{
		Sent:   sent,
		OK:     ok,
		Failed: sent - ok,
		P50:    percentile(latencies, 0.50),
		P95:    percentile(latencies, 0.95),
		P99:    percentile(latencies, 0.99),
	}
}

// percentile returns the nearest-rank p-th percentile of durs (0 <= p <=
// 1), or 0 for an empty input. durs is sorted in place; callers only ever
// pass a slice they built and don't reuse.
func percentile(durs []time.Duration, p float64) time.Duration {
	if len(durs) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// ExitCode maps a Result onto R-VER-7's existing exit-code meanings —
// smoke invents no new codes.
//
//   - 2 (error): nothing was sent at all. R-VER-7 defines 2 as "TortureU or
//     an adapter failed"; a smoke run that could not even attempt a request
//     (bad flags, the driver never fired) is that failure, not a 0-out-of-0
//     pass.
//   - 1 (fail): requests were sent but the success rate fell below
//     minSuccessRate — R-VER-7's "an assertion broke", where the assertion
//     is the constant-rate check itself.
//   - 0 (pass): requests were sent and met the threshold.
//
// R-VER-7's other two codes, 3 (aborted: unclassified egress or reset
// failure) and 4 (inconclusive: all findings ambiguous), describe concepts
// smoke has none of — no egress classification, no reset step, no
// per-finding confidence — so smoke never returns them. This gap in
// R-VER-7's coverage of smoke's own state space is reported rather than
// papered over with an invented meaning (see this task's report).
func ExitCode(r Result, minSuccessRate float64) int {
	if r.Sent == 0 {
		return 2
	}
	if r.SuccessRate() >= minSuccessRate {
		return 0
	}
	return 1
}

// Render is smoke's one human-readable rendering — it prints straight from
// Result, the same "no second formatting path" discipline R-VER-9 requires
// of `run`'s verdict, applied here even though smoke has no verdict
// document to duplicate.
func Render(url string, r Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tortureu smoke: %s\n", url)
	fmt.Fprintf(&b, "  sent: %d\n", r.Sent)
	fmt.Fprintf(&b, "  ok: %d\n", r.OK)
	fmt.Fprintf(&b, "  failed: %d\n", r.Failed)
	fmt.Fprintf(&b, "  success rate: %.1f%%\n", r.SuccessRate()*100)
	fmt.Fprintf(&b, "  p50: %s\n", r.P50)
	fmt.Fprintf(&b, "  p95: %s\n", r.P95)
	fmt.Fprintf(&b, "  p99: %s\n", r.P99)
	return b.String()
}
