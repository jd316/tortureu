// Per-verb B1 measurements. Each function drives internal/fault.Translate +
// internal/fault.Manager + the real ToxiproxyApplier/DockerApplier against a
// known-good TCP echo service through a real docker-compose stack (see
// stack.go), exactly as internal/run's own Docker-backed tests do.
//
// Per the task brief's IMPORTANT finding: torture.example.yaml and
// BENCHMARKS.md both write faults with unit-suffixed strings ("300ms",
// "1mbps", "90%"). internal/fault/translate.go copies those strings straight
// into Toxiproxy/Docker attributes with no unit parsing anywhere in
// internal/config or internal/fault. Every network-verb test below applies
// the fault EXACTLY as a human-authored torture.yaml would decode it first
// (the "primary" measurement — what a user actually gets today), and only
// afterward, separately and clearly labeled, applies an already-numeric
// version of the same fault to see whether the underlying mechanism would
// work at all if translate.go did its job. The primary result is what gets
// published as the headline verdict; the secondary is context.
package main

import (
	"fmt"
	"math"
	"time"

	"github.com/jdb316/tortureu/internal/config"
)

const pingIterations = 40

// runLatencyJitter measures both the latency and jitter table rows from one
// shared stack (they are the same Toxiproxy "latency" toxic with different
// attributes, and torture.example.yaml itself only ever writes them
// together: `inject: { latency: 300ms, jitter: 50ms }`).
func runLatencyJitter() (latency Result, jitter Result) {
	latency = Result{Verb: "latency", Requested: "+300ms", Tolerance: "±10ms (p50 delta)"}
	jitter = Result{Verb: "jitter", Requested: "σ=50ms", Tolerance: "±15% (stddev of delta)"}

	res := withStack("lat", true, "", func(s *b1Stack) Result {
		baseline, err := s.pingSamples(pingIterations)
		if err != nil {
			latency.Verdict, jitter.Verdict = "unmeasured", "unmeasured"
			latency.note("baseline measurement failed: %v", err)
			jitter.note("baseline measurement failed: %v", err)
			return Result{}
		}
		baselineStats := computeStats(baseline)

		// PRIMARY: exactly what torture.example.yaml writes — unit-suffixed
		// strings, decoded by yaml.v3 as literal Go strings, translated with
		// no unit parsing.
		primary := config.Fault{
			Name: "lat_primary", Target: echoTarget, Verb: "latency",
			Inject: map[string]any{"latency": "300ms", "jitter": "50ms"},
		}
		translated, aerr := s.applyFault(primary)
		latency.Translated, jitter.Translated = translated, translated
		if aerr != nil {
			latency.Verdict, jitter.Verdict = "miss", "miss"
			latency.note("primary (human-authored) form FAILED: %v — translate.go passed the raw YAML string %q straight to Toxiproxy's numeric \"latency\" attribute with no unit conversion", aerr, "300ms")
			jitter.note("primary (human-authored) form FAILED: %v — same root cause as the latency row: %q was never converted to milliseconds", aerr, "50ms")
		} else {
			// internal/fault now parses the unit-suffixed strings, so this
			// path is the normal one: measure BOTH rows from the same
			// after-samples, since they came from one shared toxic
			// (latency: 300ms, jitter: 50ms applied together, exactly as
			// torture.example.yaml writes them). Earlier versions of this
			// harness only filled in latency here and left jitter.Verdict
			// empty, which the caller then defaulted to "miss" — reporting
			// a confident wrong verdict for a row that was never actually
			// measured. Compute both explicitly instead.
			after, merr := s.pingSamples(pingIterations)
			s.manager.Teardown()
			if merr != nil {
				latency.Verdict, jitter.Verdict = "unmeasured", "unmeasured"
				latency.note("post-fault measurement failed: %v", merr)
				jitter.note("post-fault measurement failed: %v", merr)
			} else {
				afterStats := computeStats(after)
				delta := afterStats.P50 - baselineStats.P50
				latency.Stats = &afterStats
				latency.Measured = fmt.Sprintf("p50 delta = %.2fms", delta)
				latency.Verdict = verdictAbs(delta, 300, 10)

				deltas := make([]float64, len(after))
				for i, v := range after {
					deltas[i] = v - baselineStats.Mean
				}
				deltaStats := computeStats(deltas)
				jitter.Stats = &afterStats
				jitter.Measured = fmt.Sprintf("stddev of delta = %.2fms", deltaStats.Stddev)
				jitterPct := math.Abs(deltaStats.Stddev-50) / 50 * 100
				if jitterPct <= 15 {
					jitter.Verdict = "pass"
				} else {
					jitter.Verdict = "miss"
					jitter.note("Toxiproxy's own jitter implementation adds a UNIFORM random offset in [-jitter,+jitter] (a plain uniform(-50,50) alone would have stddev ~28.9ms, not 50ms), and any negative computed delay clamps to zero, skewing the distribution further and lowering the effective stddev even more — this is not a translate.go defect, it is what Toxiproxy itself delivers for \"jitter\" given correct numeric input. Measured stddev=%.2fms vs requested 50ms (%.1f%% off, tolerance ±15%%).", deltaStats.Stddev, jitterPct)
				}
			}
		}

		// SECONDARY: already-numeric input, clearly labeled — does the
		// mechanism work at all once a caller hands it real numbers?
		secLatency := Result{Verb: "latency", Requested: "+300ms (numeric ms, bypassing the unit-string gap)", Tolerance: "±10ms (p50 delta)"}
		fLat := config.Fault{Name: "lat_secondary", Target: echoTarget, Verb: "latency", Inject: map[string]any{"latency": 300, "jitter": 0}}
		if tr, err := s.applyFault(fLat); err != nil {
			secLatency.Verdict = "unmeasured"
			secLatency.note("numeric Apply failed: %v", err)
			secLatency.Translated = tr
		} else {
			secLatency.Translated = tr
			samples, err := s.pingSamples(pingIterations)
			s.manager.Teardown()
			if err != nil {
				secLatency.Verdict = "unmeasured"
				secLatency.note("measurement failed: %v", err)
			} else {
				st := computeStats(samples)
				delta := st.P50 - baselineStats.P50
				secLatency.Stats = &st
				secLatency.Measured = fmt.Sprintf("p50 delta = %.2fms", delta)
				secLatency.Verdict = verdictAbs(delta, 300, 10)
			}
		}
		latency.Secondary = &secLatency

		secJitter := Result{Verb: "jitter", Requested: "σ=50ms (numeric ms, bypassing the unit-string gap)", Tolerance: "±15% (stddev of delta)"}
		fJit := config.Fault{Name: "jit_secondary", Target: echoTarget, Verb: "latency", Inject: map[string]any{"latency": 0, "jitter": 50}}
		if tr, err := s.applyFault(fJit); err != nil {
			secJitter.Verdict = "unmeasured"
			secJitter.note("numeric Apply failed: %v", err)
			secJitter.Translated = tr
		} else {
			secJitter.Translated = tr
			samples, err := s.pingSamples(pingIterations)
			s.manager.Teardown()
			if err != nil {
				secJitter.Verdict = "unmeasured"
				secJitter.note("measurement failed: %v", err)
			} else {
				st := computeStats(samples)
				deltas := make([]float64, len(samples))
				for i, v := range samples {
					deltas[i] = v - baselineStats.Mean
				}
				deltaStats := computeStats(deltas)
				secJitter.Stats = &st
				secJitter.Measured = fmt.Sprintf("stddev of delta = %.2fms", deltaStats.Stddev)
				pct := math.Abs(deltaStats.Stddev-50) / 50 * 100
				secJitter.Verdict = "miss"
				if pct <= 15 {
					secJitter.Verdict = "pass"
				}
				if pct > 15 {
					secJitter.note("Toxiproxy's own jitter implementation adds a UNIFORM random offset in [-jitter,+jitter] (a plain uniform(-50,50) alone would have stddev ~28.9ms, not 50ms), and with latency=0 any negative computed delay clamps to zero, skewing the distribution further and lowering the effective stddev even more — none of this is a translate.go defect, it is what Toxiproxy itself delivers for \"jitter\" even given perfect numeric input. Measured stddev=%.2fms vs requested 50ms (%.1f%% off, tolerance ±15%%).", deltaStats.Stddev, pct)
				}
			}
		}
		jitter.Secondary = &secJitter

		// Defensive fallback only: every branch above sets both verdicts
		// explicitly. "" must never default to "miss" — that asserts the
		// fault is wrong when no number was actually measured, which is
		// exactly the bug report this comment exists to prevent from
		// recurring. If nothing set it, it truly means "no number", i.e.
		// unmeasured.
		if latency.Verdict == "" {
			latency.Verdict = "unmeasured"
			latency.note("no measurement path set a verdict — unmeasured")
		}
		if jitter.Verdict == "" {
			jitter.Verdict = "unmeasured"
			jitter.note("no measurement path set a verdict — unmeasured")
		}
		return Result{}
	})
	if res.Verdict == "unmeasured" {
		if latency.Verdict == "" {
			latency.Verdict, latency.Notes = "unmeasured", res.Notes
		}
		if jitter.Verdict == "" {
			jitter.Verdict, jitter.Notes = "unmeasured", res.Notes
		}
	}
	return latency, jitter
}

func runBandwidth() Result {
	r := Result{Verb: "bandwidth", Requested: "1 Mbps", Tolerance: "±5% (bytes/sec through the proxy)"}
	res := withStack("bw", true, "", func(s *b1Stack) Result {
		const payload = 400_000 // bytes
		// 1 Mbps = 1,000,000 bit/s = 125,000 byte/s = 125 KB/s (decimal KB,
		// which is also what internal/fault now converts "1mbps" to).
		const requestedBps = 1_000_000.0 / 8.0

		primary := config.Fault{Name: "bw_primary", Target: echoTarget, Verb: "bandwidth", Inject: map[string]any{"bandwidth": "1mbps"}}
		tr, aerr := s.applyFault(primary)
		r.Translated = tr
		if aerr != nil {
			r.Verdict = "miss"
			r.note("primary (human-authored) form FAILED: %v — translate.go passed the raw YAML string %q straight to Toxiproxy's numeric \"rate\" attribute (KB/s) with no unit parsing or Mbps->KB/s conversion", aerr, "1mbps")
		} else {
			// internal/fault now parses "1mbps" into a numeric KB/s value
			// Toxiproxy accepts, so Apply succeeding is the normal case —
			// measure it, don't stop at "it didn't error". An earlier
			// version of this harness only handled the failure path here
			// and reported "unmeasured" on success without ever running
			// the actual throughput measurement, which is a harness gap,
			// not a real "no data" situation: the fault WAS applied and
			// bytes WERE measurable.
			out, err := dockerExec(s.clientCont, "python3", "-c", bandwidthClientPy, "echo", echoPort, fmt.Sprint(payload))
			if err != nil {
				r.Verdict = "unmeasured"
				r.note("measurement exec failed: %v: %s", err, out)
			} else if obj, perr := lastJSONObject(out); perr != nil {
				r.Verdict = "unmeasured"
				r.note("could not parse measurement output: %v", perr)
			} else {
				bps, _ := obj["bytes_per_sec"].(float64)
				r.Measured = fmt.Sprintf("%.0f bytes/sec", bps)
				pct := math.Abs(bps-requestedBps) / requestedBps * 100
				r.Verdict = "miss"
				if pct <= 5 {
					r.Verdict = "pass"
				}
				r.note("measured %.0f B/s vs requested %.0f B/s (%.1f%% off, tolerance ±5%%)", bps, requestedBps, pct)
			}
		}
		s.manager.Teardown()

		// SECONDARY: already-numeric, AND already unit-converted by this
		// harness — kept for parity/comparison now that the primary path
		// also measures a real number; not required to distinguish a gap
		// anymore, since translate.go does the KB/s conversion itself.
		const requestedKBps = 125
		sec := Result{Verb: "bandwidth", Requested: fmt.Sprintf("1 Mbps == %d KB/s (numeric, unit-converted by this harness, NOT by translate.go)", requestedKBps), Tolerance: "±5% (bytes/sec through the proxy)"}
		fBw := config.Fault{Name: "bw_secondary", Target: echoTarget, Verb: "bandwidth", Inject: map[string]any{"bandwidth": requestedKBps}}
		if tr2, err := s.applyFault(fBw); err != nil {
			sec.Verdict = "unmeasured"
			sec.note("numeric Apply failed: %v", err)
			sec.Translated = tr2
		} else {
			sec.Translated = tr2
			out, err := dockerExec(s.clientCont, "python3", "-c", bandwidthClientPy, "echo", echoPort, fmt.Sprint(payload))
			s.manager.Teardown()
			if err != nil {
				sec.Verdict = "unmeasured"
				sec.note("measurement exec failed: %v: %s", err, out)
			} else if obj, perr := lastJSONObject(out); perr != nil {
				sec.Verdict = "unmeasured"
				sec.note("could not parse measurement output: %v", perr)
			} else {
				bps, _ := obj["bytes_per_sec"].(float64)
				sec.Measured = fmt.Sprintf("%.0f bytes/sec", bps)
				pct := math.Abs(bps-requestedBps) / requestedBps * 100
				sec.Verdict = "miss"
				if pct <= 5 {
					sec.Verdict = "pass"
				}
				sec.note("measured %.0f B/s vs requested %.0f B/s (%.1f%% off, tolerance ±5%%)", bps, requestedBps, pct)
			}
		}
		r.Secondary = &sec
		// Defensive fallback only (see runLatencyJitter's identical
		// comment): "" must never default to "miss" without a number
		// behind it.
		if r.Verdict == "" {
			r.Verdict = "unmeasured"
			r.note("no measurement path set a verdict — unmeasured")
		}
		return Result{}
	})
	if res.Verdict == "unmeasured" && r.Verdict == "" {
		r.Verdict, r.Notes = "unmeasured", res.Notes
	}
	return r
}

func runDown() Result {
	r := Result{Verb: "down", Requested: "connection refused", Tolerance: "exact (error class at client)"}
	res := withStack("down", true, "", func(s *b1Stack) Result {
		f := config.Fault{Name: "down_test", Target: echoTarget, Verb: "down", Inject: map[string]any{"down": true}}
		tr, err := s.applyFault(f)
		r.Translated = tr
		if err != nil {
			r.Verdict = "miss"
			r.note("Apply failed: %v", err)
			return Result{}
		}
		out, err := dockerExec(s.clientCont, "python3", "-c", downClientPy, "echo", echoPort)
		s.manager.Teardown()
		if err != nil && out == "" {
			r.Verdict = "unmeasured"
			r.note("measurement exec failed: %v", err)
			return Result{}
		}
		obj, perr := lastJSONObject(out)
		if perr != nil {
			r.Verdict = "unmeasured"
			r.note("could not parse measurement output: %v (%s)", perr, out)
			return Result{}
		}
		outcome, _ := obj["outcome"].(string)
		r.Measured = outcome
		if outcome == "refused" {
			r.Verdict = "pass"
		} else {
			r.Verdict = "miss"
			r.note("expected outcome \"refused\" (connection refused), observed %q: %v", outcome, obj)
		}
		return Result{}
	})
	if res.Verdict == "unmeasured" && r.Verdict == "" {
		r.Verdict, r.Notes = "unmeasured", res.Notes
	}
	return r
}

// pauseKillOutcome runs the shared pause/kill background-pinger protocol:
// launch a detached pinger inside the client container, let it establish a
// baseline, apply the fault at a known wall-clock instant, let it run a
// while longer, then read back every ping attempt's outcome and classify
// what happened strictly after the fault was applied.
func pauseKillOutcome(verb string, requested, expectOutcome string, tolerance string) Result {
	r := Result{Verb: verb, Requested: requested, Tolerance: tolerance}
	res := withStack(verb, false, "", func(s *b1Stack) Result {
		const outfile = "/tmp/pinger.jsonl"
		const durationS = 12
		if err := dockerExecDetached(s.clientCont, "python3", "-c", pingerBackgroundPy, "echo", echoPort, outfile, "250", fmt.Sprint(durationS)); err != nil {
			r.Verdict = "unmeasured"
			r.note("failed to launch background pinger: %v", err)
			return Result{}
		}
		time.Sleep(1500 * time.Millisecond)

		f := config.Fault{Name: verb + "_test", Target: s.echoContainer, Verb: verb, Inject: map[string]any{verb: true}}
		tr, aerr := s.applyFault(f)
		r.Translated = tr
		if aerr != nil {
			r.Verdict = "miss"
			r.note("Apply failed: %v", aerr)
			time.Sleep(time.Duration(durationS) * time.Second)
			return Result{}
		}

		time.Sleep(6 * time.Second)
		out, _ := dockerExec(s.clientCont, "cat", outfile)
		s.manager.Teardown()
		remaining := time.Duration(durationS)*time.Second - 7500*time.Millisecond
		if remaining > 0 {
			time.Sleep(remaining)
		}

		// Classify the TAIL of the log, not a timestamp-filtered subset:
		// the pinger's own wall-clock reads (Python, inside the container)
		// and this process's time.Now() (Go, on the host) are close enough
		// in practice but not reliably orderable to sub-100ms precision
		// against a 250ms poll interval — an earlier version of this
		// harness filtered on "t > applyTime+300ms" and threw away the
		// very entries that proved the fault, because docker's own
		// pause/kill command can take tens of ms to return AFTER the
		// kernel has already enforced it. The last few log lines are, by
		// construction (a 3+ second sleep after applying the fault before
		// reading the log back), deep into the post-fault window
		// regardless of clock skew.
		lines := splitJSONLines(out)
		if len(lines) == 0 {
			r.Verdict = "unmeasured"
			r.note("no ping attempts were logged at all (raw output: %q)", out)
			return Result{}
		}
		// pingerBackgroundPy stops the moment it sees "closed"/"reset"/
		// "oserror" (the kill-style terminal outcomes), so for "kill" the
		// single LAST line IS the terminal observation, however many
		// ordinary "ok" lines came before it while the fault was still in
		// flight. "pause" never breaks the loop (the connection is meant to
		// stay open, just unresponsive), so a wider tail is needed there to
		// avoid one stray pre-fault "ok" dominating a too-small sample.
		tailN := 5
		if verb == "kill" {
			tailN = 1
		}
		if tailN > len(lines) {
			tailN = len(lines)
		}
		after := lines[len(lines)-tailN:]
		r.Measured = fmt.Sprintf("last %d of %d ping attempts (tail of the post-fault observation window)", len(after), len(lines))
		allMatch := true
		outcomeCounts := map[string]int{}
		for _, m := range after {
			o, _ := m["outcome"].(string)
			outcomeCounts[o]++
			if o != expectOutcome {
				allMatch = false
			}
		}
		r.note("outcome counts after fault: %v", outcomeCounts)
		if allMatch {
			r.Verdict = "pass"
		} else {
			r.Verdict = "miss"
			r.note("expected every post-fault attempt to see outcome %q exactly, observed a mix: %v", expectOutcome, outcomeCounts)
		}
		return Result{}
	})
	if res.Verdict == "unmeasured" && r.Verdict == "" {
		r.Verdict, r.Notes = "unmeasured", res.Notes
	}
	return r
}

func runPause() Result {
	return pauseKillOutcome("pause", "no response, conn held open (SIGSTOP-equivalent freeze)", "timeout", "exact (client sees timeout not RST)")
}

func runKill() Result {
	return pauseKillOutcome("kill", "conn reset (SIGKILL)", "reset", "exact (client sees RST)")
}

func runCPU() Result {
	r := Result{Verb: "cpu", Requested: "90% of quota", Tolerance: "±5% (cgroup cpu.stat)"}
	res := withStack("cpu", false, cpuEchoImage, func(s *b1Stack) Result {
		if _, err := dockerExec(s.echoContainer, "test", "-f", "/sys/fs/cgroup/cpu.stat"); err != nil {
			r.Verdict = "unmeasured"
			r.note("cgroup v2 cpu.stat not reachable inside the container: %v", err)
			return Result{}
		}
		before, err := dockerExec(s.echoContainer, "cat", "/sys/fs/cgroup/cpu.stat")
		if err != nil {
			r.Verdict = "unmeasured"
			r.note("reading cpu.stat before the fault failed: %v", err)
			return Result{}
		}
		usageBefore, err := cpuStatUsageUsec(before)
		if err != nil {
			r.Verdict = "unmeasured"
			r.note("parsing cpu.stat before the fault failed: %v", err)
			return Result{}
		}
		t0 := time.Now()

		f := config.Fault{Name: "cpu_test", Target: s.echoContainer, Verb: "cpu", Inject: map[string]any{"cpu": "90%", "workers": 4}}
		tr, aerr := s.applyFault(f)
		r.Translated = tr
		if aerr != nil {
			r.Verdict = "miss"
			r.note("Apply failed: %v", aerr)
			return Result{}
		}

		time.Sleep(5 * time.Second)
		t1 := time.Now()
		after, err := dockerExec(s.echoContainer, "cat", "/sys/fs/cgroup/cpu.stat")
		s.manager.Teardown()
		if err != nil {
			r.Verdict = "unmeasured"
			r.note("reading cpu.stat after the fault failed: %v", err)
			return Result{}
		}
		usageAfter, err := cpuStatUsageUsec(after)
		if err != nil {
			r.Verdict = "unmeasured"
			r.note("parsing cpu.stat after the fault failed: %v", err)
			return Result{}
		}

		deltaUsageSec := float64(usageAfter-usageBefore) / 1e6
		wallSec := t1.Sub(t0).Seconds()
		measuredPercent := deltaUsageSec / wallSec * 100
		r.Measured = fmt.Sprintf("%.1f%% of one core (cgroup cpu.stat usage_usec delta / wall time)", measuredPercent)
		pct := math.Abs(measuredPercent-90) / 90 * 100
		r.Verdict = "miss"
		if pct <= 5 {
			r.Verdict = "pass"
		}
		r.note("translateDocker's \"cpu\" case sets Args[\"amount\"]=f.Inject[\"cpu\"] (%q), but DockerApplier.ApplyDocker's \"stress\" case never reads Args[\"amount\"] at all — it only reads Args[\"workers\"] and runs `stress-ng --cpu <workers>` with no load percentage. Requested 90%%, measured %.1f%% (4 unthrottled stress-ng workers, no cgroup CPU quota set for the cpu verb).", "90%", measuredPercent)
		return Result{}
	})
	if res.Verdict == "unmeasured" && r.Verdict == "" {
		r.Verdict, r.Notes = "unmeasured", res.Notes
	}
	return r
}

// verdictAbs classifies an absolute-difference tolerance check.
func verdictAbs(measured, target, tolerance float64) string {
	if math.Abs(measured-target) <= tolerance {
		return "pass"
	}
	return "miss"
}

// pingSamples execs pingClientPy once (the whole N-iteration loop inside one
// docker exec call, per the task brief, so per-iteration process-spawn
// overhead never swamps the ±10ms/±15% tolerances) and returns the parsed
// millisecond samples.
func (s *b1Stack) pingSamples(n int) ([]float64, error) {
	out, err := dockerExec(s.clientCont, "python3", "-c", pingClientPy, "echo", echoPort, fmt.Sprint(n))
	if err != nil {
		return nil, fmt.Errorf("exec: %w: %s", err, out)
	}
	obj, err := lastJSONObject(out)
	if err != nil {
		return nil, err
	}
	samples := float64s(obj["samples"])
	if len(samples) == 0 {
		return nil, fmt.Errorf("no samples in output: %s", out)
	}
	return samples, nil
}

// splitJSONLines parses every JSON-object line in raw, skipping anything
// that fails to parse.
func splitJSONLines(raw string) []map[string]any {
	var out []map[string]any
	for _, line := range splitLines(raw) {
		obj, err := lastJSONObject(line)
		if err == nil {
			out = append(out, obj)
		}
	}
	return out
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
