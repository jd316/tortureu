package trace

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// degradationFactor is how much slower the fault target's p95 must be than
// its own baseline before a chain is built at all (R-VER-13).
//
// This gate is the whole difference between evidence and decoration. A repo
// with tracing always has spans; spans always have a request path; so a
// chain could always be drawn. What makes a chain worth reading is that the
// dependency the fault targeted actually got slower on that path. 2x is
// deliberately a low bar — the smallest step that cannot be read as
// ordinary run-to-run noise — because the chain is evidence *offered to* a
// reader alongside the fault declaration, not a claim made in place of one.
const degradationFactor = 2

// peerAttrs are the span attributes that name which peer a client span
// talked to, in the order they are consulted. Both the current OTel
// semantic conventions (server.address / server.port) and the older,
// still overwhelmingly deployed ones (net.peer.*) are read: an
// instrumentation library pinned two years ago emits the latter and its
// spans are no less real.
var peerAttrs = []string{"server.address", "net.peer.name", "peer.service", "net.sock.peer.name", "net.peer.ip", "peer.hostname"}

// peerPortAttrs are the corresponding port attributes.
var peerPortAttrs = []string{"server.port", "net.peer.port", "net.sock.peer.port"}

// BuildChain derives VERDICT.md §1's `chain` for a fault whose target is
// the given "host:port", from real spans (R-VER-13). It returns nil —
// never a partial or plausible chain — whenever the evidence is not there:
// no span touches the target, or the target shows no measured degradation.
//
// Derivation, in order:
//
//  1. Find every span matching the target (matchesTarget).
//  2. Take the slowest of them as the representative: the fault's effect is
//     what we are trying to show, so the worst real observation is the one
//     to walk, and it is a real observation rather than a synthesized one.
//  3. Measure that hop: baseline is the median of the fastest quartile of
//     every span sharing its service+operation across all sampled traces;
//     degraded is their p95. Refuse if degraded < 2x baseline.
//  4. Walk parent links from the representative span to the trace root,
//     measuring each ancestor the same way.
//
// The result is ordered fault -> symptom (target first, root last), which
// is the order VERDICT.md's own example uses and the order a reader follows
// when asking "and then what happened".
func BuildChain(traces []Trace, target string) []Hop {
	host, port := splitTarget(target)
	if host == "" {
		return nil
	}

	stats := measure(traces)

	var best *Span
	var bestTrace *Trace
	for ti := range traces {
		for si := range traces[ti].Spans {
			s := &traces[ti].Spans[si]
			if !matchesTarget(s, host, port) {
				continue
			}
			if best == nil || s.Duration > best.Duration {
				best, bestTrace = s, &traces[ti]
			}
		}
	}
	if best == nil {
		return nil
	}

	targetStat, ok := stats[statKey(best)]
	if !ok || targetStat.degraded < time.Duration(degradationFactor)*targetStat.baseline {
		return nil
	}

	byID := make(map[string]*Span, len(bestTrace.Spans))
	for i := range bestTrace.Spans {
		byID[bestTrace.Spans[i].SpanID] = &bestTrace.Spans[i]
	}

	hops := []Hop{{At: target, Observed: targetStat.observed()}}
	seen := map[string]bool{best.SpanID: true}
	for cur := best; cur.ParentID != ""; {
		parent, ok := byID[cur.ParentID]
		if !ok || seen[parent.SpanID] {
			break
		}
		seen[parent.SpanID] = true
		st, ok := stats[statKey(parent)]
		if !ok {
			break
		}
		hops = append(hops, Hop{
			At:       strings.TrimSpace(parent.Service + " " + parent.Operation),
			Observed: st.observed(),
		})
		cur = parent
	}
	return hops
}

// stat is one service+operation's measured latency across every sampled
// span with that key. Nothing here is modelled or extrapolated: baseline
// and degraded are both order statistics of durations a backend returned.
type stat struct {
	baseline time.Duration // median of the fastest quartile
	degraded time.Duration // p95
	n        int
}

func (s stat) observed() string {
	return fmt.Sprintf("latency %s -> %s (n=%d spans)", formatDur(s.baseline), formatDur(s.degraded), s.n)
}

func statKey(s *Span) string { return s.Service + "\x00" + s.Operation }

// measure collects the per-service+operation latency statistics every hop's
// `observed` is rendered from. Computing the baseline as the *fastest
// quartile* of the run rather than as "spans before the fault started" is
// deliberate and is the honest option available here: this package is given
// spans, not the wall-clock moment a fault was applied (see SPEC.md §12
// TBD-9 for why that timestamp does not reach here). The fastest quartile
// is a real measurement of the same operation in the same run, which is
// exactly what "4ms -> 304ms" claims it is.
func measure(traces []Trace) map[string]stat {
	durations := map[string][]time.Duration{}
	for ti := range traces {
		for si := range traces[ti].Spans {
			s := &traces[ti].Spans[si]
			durations[statKey(s)] = append(durations[statKey(s)], s.Duration)
		}
	}
	out := make(map[string]stat, len(durations))
	for key, ds := range durations {
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		out[key] = stat{baseline: fastQuartileMedian(ds), degraded: percentile(ds, 0.95), n: len(ds)}
	}
	return out
}

// fastQuartileMedian is the median of the fastest quarter of ds, which must
// already be sorted ascending. With fewer than four samples the fastest
// quartile is the single fastest span — still a real observation, and the
// caller's degradation gate is what decides whether so few samples say
// anything.
func fastQuartileMedian(ds []time.Duration) time.Duration {
	n := len(ds) / 4
	if n < 1 {
		n = 1
	}
	return ds[n/2]
}

// percentile is the nearest-rank percentile of a sorted slice.
func percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	i := int(p * float64(len(ds)-1))
	return ds[i]
}

// matchesTarget reports whether span observed a call to host[:port]. Two
// shapes count, and both are real: the span *is* the dependency (an
// instrumented database reports itself as its own service), or the span is
// a client span in the caller naming that peer in its attributes.
//
// A port stated in the span and different from the target's is a mismatch —
// two dependencies on the same host are two different dependencies. A port
// the span does not state is not a mismatch: most instrumentation omits it,
// and refusing on a missing attribute would reject almost every real span.
func matchesTarget(s *Span, host, port string) bool {
	if s.Service == host {
		return portOK(s, port)
	}
	for _, key := range peerAttrs {
		v, ok := s.Attrs[key]
		if !ok {
			continue
		}
		h, p := splitTarget(v)
		if h != host {
			continue
		}
		if p != "" {
			return port == "" || p == port
		}
		return portOK(s, port)
	}
	return false
}

func portOK(s *Span, port string) bool {
	if port == "" {
		return true
	}
	for _, key := range peerPortAttrs {
		if v, ok := s.Attrs[key]; ok && v != "" {
			return v == port
		}
	}
	return true
}

// splitTarget splits "host:port" into its parts; a bare host yields an
// empty port.
func splitTarget(target string) (host, port string) {
	target = strings.TrimSpace(target)
	i := strings.LastIndex(target, ":")
	if i < 0 {
		return target, ""
	}
	host, port = target[:i], target[i+1:]
	if _, err := strconv.Atoi(port); err != nil {
		return target, ""
	}
	return host, port
}

// formatDur renders a duration the way VERDICT.md's own chain example does
// — milliseconds — falling back to microseconds below 1ms so a sub-
// millisecond baseline does not render as the "0ms" that would make every
// ratio meaningless to a reader.
func formatDur(d time.Duration) string {
	if d < time.Millisecond {
		return strconv.FormatInt(int64(d/time.Microsecond), 10) + "µs"
	}
	ms := float64(d) / float64(time.Millisecond)
	return strings.TrimSuffix(strings.TrimRight(strconv.FormatFloat(ms, 'f', 1, 64), "0"), ".") + "ms"
}
