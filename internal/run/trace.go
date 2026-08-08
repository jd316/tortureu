package run

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/trace"
	"github.com/jdb316/tortureu/internal/verdict"
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
	src, err := trace.Open(ctx, url, &http.Client{Timeout: traceQueryTimeout})
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

// applyTraceChain fills in a finding's Chain from real ingested spans and,
// only when it succeeds, raises the finding's confidence to `caused` under
// the observability ceiling (R-VER-13, R-VER-14).
//
// Every early return leaves the finding exactly as R-VER-3's fault-count
// rule left it — empty chain, `correlated` or `ambiguous` — which is the
// behaviour this codebase had before ingestion existed and the behaviour it
// must fall back to whenever the evidence is not there.
func applyTraceChain(f *verdict.Finding, sys detect.System) {
	if f.Cause == nil || f.Cause.Target == "" {
		// No single fault identified a target, so there is nothing to chain
		// *to*. Note this is checked before the backend is consulted: with
		// no target, a query could only fish for a story.
		return
	}
	service := os.Getenv(traceServiceEnv)
	if service == "" {
		service = sys.SUT
	}
	if service == "" {
		return
	}
	src := traceSourceFor(sys)
	if src == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), traceQueryTimeout)
	defer cancel()
	traces, err := src.Traces(ctx, service, processStart, time.Now(), traceQueryLimit)
	if err != nil || len(traces) == 0 {
		return
	}
	hops := trace.BuildChain(traces, f.Cause.Target)
	if len(hops) == 0 {
		return
	}
	f.Chain = make([]verdict.ChainHop, 0, len(hops))
	for _, h := range hops {
		f.Chain = append(f.Chain, verdict.ChainHop{At: h.At, Observed: h.Observed})
	}
	f.Confidence = verdict.Caused.AtMost(verdict.Confidence(sys.Obs.MaxConfidence))
}
