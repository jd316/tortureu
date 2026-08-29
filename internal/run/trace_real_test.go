// trace_real_test.go exercises R-VER-17's multi-fault attribution against a
// *live* Jaeger holding real spans, through the production ingestion path
// (defaultTraceSource -> internal/trace.Jaeger -> BuildChain) rather than a
// substituted source. internal/run's other trace tests prove the decision
// rule against in-memory spans; this one proves the same rule still holds
// when the spans arrive over the wire from a server.
//
// It skips unless TORTUREU_TRACE_URL points at a reachable backend, because
// a developer machine is not required to have one. Reproduce with:
//
//	docker run -d -p 16686:16686 -p 4318:4318 jaegertracing/jaeger:2.10.0
//	# export OTLP spans for a service calling postgres:5432 and redis:6379,
//	# with exactly one of them degraded
//	TORTUREU_TRACE_URL=http://localhost:16686 \
//	  TORTUREU_TRACE_SERVICE=one-degraded \
//	  TORTUREU_TRACE_EXPECT_FAULT=pg_slow \
//	  go test ./internal/run/ -run RealBackendMultiFault -v
//
// TORTUREU_TRACE_EXPECT_FAULT names the fault the ingested spans must
// attribute the finding to; setting it to "none" asserts the negative case —
// the finding must stay ambiguous with no cause at all.
package run

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/verdict"
)

// spec: R-VER-17
func TestRealBackendMultiFaultAttribution(t *testing.T) {
	if os.Getenv(traceURLEnv) == "" {
		t.Skip("TORTUREU_TRACE_URL not set: no live trace backend to query")
	}
	service := os.Getenv(traceServiceEnv)
	if service == "" {
		t.Skip("TORTUREU_TRACE_SERVICE not set: nothing to scope the query to")
	}
	want := os.Getenv("TORTUREU_TRACE_EXPECT_FAULT")
	if want == "" {
		t.Skip("TORTUREU_TRACE_EXPECT_FAULT not set: nothing asserted")
	}

	// The spans were exported into the backend before this process started,
	// so the run window this package normally queries over (processStart,
	// captured at package init) would exclude them. Widening it is what
	// makes pre-loaded spans readable at all; it changes nothing about the
	// decision rule under test, which is a measured step at a target.
	prev := processStart
	processStart = time.Now().Add(-time.Hour)
	t.Cleanup(func() { processStart = prev })

	// The multi-fault schedule to attribute from. Overridable so the same
	// test can be pointed at a live stack whose dependencies are not the
	// default postgres/redis pair — E1's case 9, for instance.
	faults := []config.Fault{
		{Name: "pg_slow", Target: "postgres:5432", Verb: "latency", At: "peak", For: "15s"},
		{Name: "redis_slow", Target: "redis:6379", Verb: "latency", At: "peak", For: "15s"},
	}
	if spec := os.Getenv("TORTUREU_TRACE_FAULTS"); spec != "" {
		faults = nil
		for _, entry := range strings.Split(spec, ",") {
			name, target, ok := strings.Cut(strings.TrimSpace(entry), "=")
			if !ok {
				t.Fatalf("TORTUREU_TRACE_FAULTS entry %q: want name=host:port", entry)
			}
			faults = append(faults, config.Fault{Name: name, Target: target, Verb: "latency", At: "peak", For: "15s"})
		}
	}
	sys := detect.System{SUT: service, Obs: detect.Obs{Traces: true, MaxConfidence: "caused"}}

	_, findings := evaluateThresholds(brokenThreshold(), faults, sys, nil)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want 1", findings)
	}
	f := findings[0]

	if want == "none" {
		if f.Cause != nil {
			t.Fatalf("cause = %+v, want none from these live spans", *f.Cause)
		}
		if f.Chain != nil {
			t.Errorf("chain = %+v, want empty with no attributed cause", f.Chain)
		}
		if f.Confidence != verdict.Ambiguous {
			t.Errorf("confidence = %q, want ambiguous", f.Confidence)
		}
		return
	}

	if f.Cause == nil {
		t.Fatalf("cause = nil, want %q attributed from the live spans", want)
	}
	if f.Cause.Fault != want {
		t.Errorf("cause.fault = %q, want %q", f.Cause.Fault, want)
	}
	if len(f.Chain) == 0 {
		t.Fatalf("chain empty: an attributed cause must carry the chain it was read from")
	}
	for i, h := range f.Chain {
		t.Logf("hop %d: at=%q observed=%q", i, h.At, h.Observed)
	}
	if f.Confidence != verdict.Caused {
		t.Errorf("confidence = %q, want caused", f.Confidence)
	}
}
