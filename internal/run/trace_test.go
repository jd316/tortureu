package run

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/trace"
	"github.com/jdb316/tortureu/internal/verdict"
)

// fakeTraceSource stands in for a real Jaeger. The spans it returns are the
// same shape internal/trace parses out of a real jaegertracing/jaeger
// response (proved by internal/trace's own fixture test); this test is
// about what internal/run does with them.
type fakeTraceSource struct {
	traces []trace.Trace
	err    error
	calls  int
}

func (f *fakeTraceSource) Traces(_ context.Context, _ string, _, _ time.Time, _ int) ([]trace.Trace, error) {
	f.calls++
	return f.traces, f.err
}

// degradingTraces are 20 traces through checkout-api -> postgres:5432,
// the last half of them slow, so the target hop shows a real measured step.
func degradingTraces() []trace.Trace {
	base := time.Unix(1700000000, 0)
	var out []trace.Trace
	for i := 0; i < 20; i++ {
		d := 4 * time.Millisecond
		if i >= 10 {
			d = 304 * time.Millisecond
		}
		out = append(out, trace.Trace{ID: "t", Spans: []trace.Span{
			{SpanID: "a", Service: "checkout-api", Operation: "POST /checkout", Start: base, Duration: d + 20*time.Millisecond},
			{SpanID: "b", ParentID: "a", Service: "checkout-api", Operation: "SELECT orders", Start: base, Duration: d,
				Attrs: map[string]string{"net.peer.name": "postgres", "net.peer.port": "5432"}},
		}})
	}
	return out
}

// flatTraces are the same request path with no degradation at all.
func flatTraces() []trace.Trace {
	base := time.Unix(1700000000, 0)
	var out []trace.Trace
	for i := 0; i < 20; i++ {
		out = append(out, trace.Trace{ID: "t", Spans: []trace.Span{
			{SpanID: "a", Service: "checkout-api", Operation: "POST /checkout", Start: base, Duration: 24 * time.Millisecond},
			{SpanID: "b", ParentID: "a", Service: "checkout-api", Operation: "SELECT orders", Start: base, Duration: 4 * time.Millisecond,
				Attrs: map[string]string{"net.peer.name": "postgres", "net.peer.port": "5432"}},
		}})
	}
	return out
}

func withTraceSource(t *testing.T, src trace.Source) {
	t.Helper()
	prev := traceSourceFor
	traceSourceFor = func(detect.System) trace.Source { return src }
	t.Cleanup(func() { traceSourceFor = prev })
}

func brokenThreshold() map[string]any {
	return map[string]any{
		"http_req_duration": map[string]any{
			"contains":   "time",
			"p(99)":      float64(4218),
			"thresholds": map[string]any{"p(99)<1500": map[string]any{"ok": false}},
		},
	}
}

// pgFault is one fault against the dependency the fake spans traverse.
func pgFault() []config.Fault {
	return []config.Fault{{Name: "pg_slow", Target: "postgres:5432", Verb: "latency", At: "peak"}}
}

func tracedSystem() detect.System {
	return detect.System{
		SUT: "checkout-api",
		Obs: detect.Obs{Traces: true, MaxConfidence: "caused"},
	}
}

// spec: R-VER-13
// spec: R-VER-14
func TestTraceChainRaisesConfidenceToCaused(t *testing.T) {
	withTraceSource(t, &fakeTraceSource{traces: degradingTraces()})

	_, findings := evaluateThresholds(brokenThreshold(), pgFault(), tracedSystem(), nil)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want 1", findings)
	}
	f := findings[0]
	if f.Confidence != verdict.Caused {
		t.Errorf("confidence = %q, want caused: a real span path through the fault target was ingested", f.Confidence)
	}
	if len(f.Chain) < 2 {
		t.Fatalf("chain = %+v, want the ingested hop list", f.Chain)
	}
	if f.Chain[0].At != "postgres:5432" {
		t.Errorf("chain[0].At = %q, want the fault target", f.Chain[0].At)
	}
	if f.Chain[0].Observed == "" {
		t.Error("chain[0].Observed empty: every hop must carry a measured value")
	}
}

// spec: R-VER-13
func TestNoTraceSourceLeavesChainEmptyAndConfidenceCorrelated(t *testing.T) {
	withTraceSource(t, nil)

	_, findings := evaluateThresholds(brokenThreshold(), pgFault(), tracedSystem(), nil)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want 1", findings)
	}
	if findings[0].Chain != nil {
		t.Errorf("chain = %+v, want empty with no ingestible backend", findings[0].Chain)
	}
	if findings[0].Confidence != verdict.Correlated {
		t.Errorf("confidence = %q, want correlated", findings[0].Confidence)
	}
}

// spec: R-VER-13
func TestNoObservedDegradationLeavesChainEmpty(t *testing.T) {
	withTraceSource(t, &fakeTraceSource{traces: flatTraces()})

	_, findings := evaluateThresholds(brokenThreshold(), pgFault(), tracedSystem(), nil)
	if findings[0].Chain != nil {
		t.Errorf("chain = %+v, want empty: the target never degraded", findings[0].Chain)
	}
	if findings[0].Confidence != verdict.Correlated {
		t.Errorf("confidence = %q, want correlated", findings[0].Confidence)
	}
}

// spec: R-VER-13
func TestTraceQueryErrorLeavesChainEmpty(t *testing.T) {
	withTraceSource(t, &fakeTraceSource{err: errors.New("connection refused")})

	_, findings := evaluateThresholds(brokenThreshold(), pgFault(), tracedSystem(), nil)
	if findings[0].Chain != nil {
		t.Errorf("chain = %+v, want empty when the backend could not be read", findings[0].Chain)
	}
	if findings[0].Confidence != verdict.Correlated {
		t.Errorf("confidence = %q, want correlated", findings[0].Confidence)
	}
}

// spec: R-VER-14
func TestChainNeverExceedsTheObservabilityCeiling(t *testing.T) {
	withTraceSource(t, &fakeTraceSource{traces: degradingTraces()})

	sys := tracedSystem()
	sys.Obs.MaxConfidence = "correlated"
	_, findings := evaluateThresholds(brokenThreshold(), pgFault(), sys, nil)
	if findings[0].Confidence != verdict.Correlated {
		t.Errorf("confidence = %q, want correlated: Obs.MaxConfidence is a ceiling", findings[0].Confidence)
	}
	if len(findings[0].Chain) < 2 {
		t.Errorf("chain = %+v: the ingested hops are still real and must still be reported", findings[0].Chain)
	}
}

// spec: R-VER-13
func TestNoFaultMeansNoTargetSoNoChain(t *testing.T) {
	src := &fakeTraceSource{traces: degradingTraces()}
	withTraceSource(t, src)

	_, findings := evaluateThresholds(brokenThreshold(), nil, tracedSystem(), nil)
	if findings[0].Chain != nil {
		t.Errorf("chain = %+v, want empty: no fault identifies a target to chain from", findings[0].Chain)
	}
	if src.calls != 0 {
		t.Errorf("backend queried %d times with no target to look for", src.calls)
	}
}

// spec: R-VER-13
func TestTraceIngestionRefusedWhenDetectionSawNoTraces(t *testing.T) {
	// defaultTraceSource is the production resolver; with no explicit
	// endpoint it must refuse outright on a repo detection says has no
	// tracing backend, rather than probing localhost.
	t.Setenv(traceURLEnv, "")
	sys := detect.System{SUT: "checkout-api"}
	sys.Coverage.LacksOtel = detect.FactTrue
	if src := defaultTraceSource(sys); src != nil {
		t.Errorf("got source %T, want none: detection reported no traces and lacks:otel", src)
	}
}
