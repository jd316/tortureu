package run

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/trace"
	"github.com/jd316/tortureu/internal/verdict"
)

// traceURLEnv names the trace backend to ingest from (R-VER-13). It is an
// environment variable rather than a torture.yaml key or a CLI flag because
// it is a property of the machine the run happens on — the published port
// of a developer's Jaeger, or a shared one in CI — not of the scenario,
// which is the thing torture.yaml describes and which is committed.
const traceURLEnv = "TORTUREU_TRACE_URL"

// traceServiceEnv overrides which service name the backend is queried for.
// The default is the compose service name of the system under test
// (detect.System.SUT), which is what R-DET-8 gives us; a repo whose OTel
// `service.name` differs from its compose service name needs this, and a
// wrong service name produces no traces and therefore no chain rather than
// a wrong one.
const traceServiceEnv = "TORTUREU_TRACE_SERVICE"

// defaultJaegerURL is where a compose stack that publishes Jaeger's UI port
// puts it. It is tried only when detection already reported a tracing
// backend (see defaultTraceSource): probing localhost on a repo with no
// tracing at all would be a request nobody asked for, and an unrelated
// Jaeger on a developer's machine must not be able to contribute spans to a
// verdict about a different system.
const defaultJaegerURL = "http://localhost:16686"

// traceQueryLimit caps how many traces one query samples. Large enough that
// the per-hop baseline/p95 are computed over a meaningful population, small
// enough that a chain never becomes the expensive part of a run.
const traceQueryLimit = 200

// traceQueryTimeout bounds the whole ingestion attempt. A slow or wedged
// backend must cost the run a bounded pause and then produce no chain — it
// must never hold a verdict open.
const traceQueryTimeout = 15 * time.Second

// processStart is captured at package initialization, and is the start of
// the window traces are queried over.
//
// It is a deliberate over-approximation of the run window, and the reason
// is worth stating: the wall-clock moment each fault was applied lives in
// this package's scheduler and is not carried into finding evaluation, so
// the exact fault window is not available here (SPEC.md §12, TBD-9). A
// superset window can only ever *dilute* the measured step at the target —
// it mixes pre-fault spans into the same population — which is why the
// gate that decides whether a chain exists at all is a measured
// degradation inside the window (R-VER-13) rather than the window's own
// bounds.
var processStart = time.Now()

// traceSourceFor resolves the trace backend to ingest from for this system,
// or nil when there is none to ingest from. It is a variable so tests can
// substitute a source without a live backend; production always uses
// defaultTraceSource.
var traceSourceFor = defaultTraceSource

// traceSourceOnce memoizes the resolution: it performs network probes, and
// a run evaluates many findings.
var (
	traceSourceOnce   sync.Once
	memoizedSource    trace.Source
	memoizedSourceErr error
)

// defaultTraceSource decides whether ingestion may run at all, and against
// what (R-VER-13).
//
// It consumes detection's answers rather than recomputing them: R-DET-12
// already classified `jaeger*`/`tempo*`/`otel/opentelemetry-collector*`
// images as tracing infrastructure (detect.Obs.Traces), and R-COV-6's
// tri-state lacks:otel already says whether any OTel client or collector
// was found at all. Re-deriving either here would be a second, divergent
// detector for a fact the system already has.
//
// An explicitly configured endpoint bypasses those gates on purpose: the
// user naming a backend is a stronger statement than an inference from
// compose, and it is the escape hatch for the cases detection cannot see
// (a backend outside compose, a Kubernetes-hosted collector, a repo whose
// manifest could not be read).
func defaultTraceSource(sys detect.System) trace.Source {
	traceSourceOnce.Do(func() {
		memoizedSource, memoizedSourceErr = resolveTraceSource(sys)
	})
	if memoizedSourceErr != nil {
		// Reported once, on stderr, never swallowed: a user who pointed us
		// at a Tempo has to learn that is why their findings say
		// `correlated` (R-VER-13's refusal-must-be-spoken rule). It is not
		// a run failure — a verdict without a chain is exactly the verdict
		// this tool emitted before ingestion existed.
		reportTraceRefusalOnce(memoizedSourceErr)
	}
	return memoizedSource
}

var traceRefusalOnce sync.Once

func reportTraceRefusalOnce(err error) {
	traceRefusalOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "tortureu: no causal chain will be built: %v\n", err)
	})
}

func resolveTraceSource(sys detect.System) (trace.Source, error) {
	url := os.Getenv(traceURLEnv)
	explicit := url != ""
	if !explicit {
		if !sys.Obs.Traces || sys.Coverage.LacksOtel == detect.FactTrue {
			// Nothing detection saw produces readable spans. Silence here
			// is correct rather than a refusal to report: R-VER-11 already
			// puts the reason in every verdict, as the observability block
			// and its `correlated` ceiling.
			return nil, nil
		}
		url = defaultJaegerURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), traceQueryTimeout)
	defer cancel()
	src, err := trace.Open(ctx, url, traceHTTPClient(explicit))
	if err != nil {
		if !explicit {
			// We guessed the endpoint; nothing is listening, or what is
			// listening is not a Jaeger. That is not something to shout
			// about, because the user never claimed there was one there.
			return nil, nil
		}
		return nil, err
	}
	return src, nil
}

// traceHTTPClient builds the client the trace backend is queried with.
//
// An explicitly configured endpoint gets fallbackTransport (inreach.go):
// a direct call first, falling back to a tunnel through the target
// container's own network namespace. Without it, ingestion could not work
// on a DC-2-isolated stack at all — measured on E1's case 9, where the
// topology overlay leaves the compose stack's Jaeger with no published
// host port, so `http://jaeger:16686` is unresolvable from the
// orchestrator process and every finding stayed unattributed. This is the
// same reachability problem, and the same fix, as the Prometheus and
// broker clients (promql.go, run.go).
//
// The guessed localhost endpoint deliberately does not get it: nobody
// asked for that probe, and spawning a tunnel container to chase an
// address the user never named would be a cost (and a surprise) for a
// request that is only ever a maybe.
func traceHTTPClient(explicit bool) *http.Client {
	c := &http.Client{Timeout: traceQueryTimeout}
	if explicit {
		c.Transport = fallbackTransport{}
	}
	return c
}

// ingestTraces reads this run's traces for the system under test, or
// returns nothing at all when there is no readable backend (R-VER-13). It
// is separated from applyTraceChain because two different questions are
// now asked of the same spans: which hops a known target's chain has, and
// — when several faults were active — which target degraded at all
// (R-VER-17). Both must be answered from one query's worth of evidence.
func ingestTraces(sys detect.System) []trace.Trace {
	service := os.Getenv(traceServiceEnv)
	if service == "" {
		service = sys.SUT
	}
	if service == "" {
		return nil
	}
	src := traceSourceFor(sys)
	if src == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), traceQueryTimeout)
	defer cancel()
	traces, err := src.Traces(ctx, service, processStart, time.Now(), traceQueryLimit)
	if err != nil {
		return nil
	}
	return traces
}

// hasAddressTarget reports whether any active fault names a "host:port"
// target — i.e. whether there is anything the spans could be asked about
// at all. It gates the query itself: with nothing to look for, a request to
// the backend could only fish for a story.
func hasAddressTarget(faults []config.Fault) bool {
	for _, f := range faults {
		if trace.IsAddress(f.Target) {
			return true
		}
	}
	return false
}

// soleDegradedFault implements R-VER-17's decision rule: among the active
// faults whose targets are addresses, return the one — and only if there is
// exactly one — whose target shows measured degradation in these spans,
// together with the chain built from it.
//
// "Degraded" is not a second, weaker test invented here: it is exactly
// R-VER-13's own gate, since trace.BuildChain returns hops only when a real
// span matches the target and its p95 is at least twice its own
// fastest-quartile baseline. Reusing it is what makes the attribution and
// the chain the same piece of evidence rather than two claims that could
// disagree.
//
// Two degraded targets, no degraded target, or two faults sharing the one
// degraded target (degradation cannot tell those two faults apart) all
// return nil — the finding then stays `ambiguous` with no cause, which is
// the honest answer and the behaviour this code had before.
func soleDegradedFault(traces []trace.Trace, faults []config.Fault) (*config.Fault, []trace.Hop) {
	var found *config.Fault
	var foundHops []trace.Hop
	chains := map[string][]trace.Hop{}
	for i := range faults {
		target := faults[i].Target
		if !trace.IsAddress(target) {
			continue
		}
		hops, done := chains[target]
		if !done {
			hops = trace.BuildChain(traces, target)
			chains[target] = hops
		}
		if len(hops) == 0 {
			continue
		}
		if found != nil {
			return nil, nil
		}
		found, foundHops = &faults[i], hops
	}
	return found, foundHops
}

// applyTraceChain fills in a finding's Chain from real ingested spans —
// and, when several faults were active and no single one could be named
// from the schedule alone, its Cause too (R-VER-17) — then, only when a
// chain was actually built, raises the finding's confidence to `caused`
// under the observability ceiling (R-VER-13, R-VER-14).
//
// Every early return leaves the finding exactly as R-VER-3's fault-count
// rule left it — no cause, empty chain, `correlated` or `ambiguous` — which
// is the behaviour this codebase had before ingestion existed and the
// behaviour it must fall back to whenever the evidence is not there.
func applyTraceChain(f *verdict.Finding, sys detect.System, faults []config.Fault) {
	if f.Cause != nil && f.Cause.Target == "" {
		// A single fault was active but it names no address (a queue
		// fault's topic), so there is nothing to chain *to*.
		return
	}
	if f.Cause == nil && !hasAddressTarget(faults) {
		// Nothing to attribute to and nothing to chain from: with zero
		// faults, or only non-address targets, the backend is not
		// consulted at all.
		return
	}
	traces := ingestTraces(sys)
	if len(traces) == 0 {
		return
	}
	var hops []trace.Hop
	if f.Cause == nil {
		cause, causeHops := soleDegradedFault(traces, faults)
		if cause == nil {
			return
		}
		f.Cause = &verdict.Cause{
			Fault:  cause.Name,
			Target: cause.Target,
			Inject: cause.Inject,
			Window: faultWindow(*cause),
		}
		hops = causeHops
	} else {
		hops = trace.BuildChain(traces, f.Cause.Target)
	}
	if len(hops) == 0 {
		return
	}
	f.Chain = make([]verdict.ChainHop, 0, len(hops))
	for _, h := range hops {
		f.Chain = append(f.Chain, verdict.ChainHop{At: h.At, Observed: h.Observed})
	}
	f.Confidence = verdict.Caused.AtMost(verdict.Confidence(sys.Obs.MaxConfidence))
}
