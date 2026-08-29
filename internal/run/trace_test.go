package run

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/trace"
	"github.com/jd316/tortureu/internal/verdict"
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

// twoDepTraces are 20 traces through checkout-api that call two
// dependencies on every request — postgres:5432 and redis:6379 — with
// independent control over which of them actually got slower. This is the
// shape R-VER-17 is about: several faults were active, and only the spans
// say which dependency the load actually went bad at.
func twoDepTraces(slowPG, slowRedis bool) []trace.Trace {
	base := time.Unix(1700000000, 0)
	step := func(slow bool, i int) time.Duration {
		if slow && i >= 10 {
			return 304 * time.Millisecond
		}
		return 4 * time.Millisecond
	}
	var out []trace.Trace
	for i := 0; i < 20; i++ {
		out = append(out, trace.Trace{ID: "t", Spans: []trace.Span{
			{SpanID: "a", Service: "checkout-api", Operation: "POST /checkout", Start: base, Duration: 500 * time.Millisecond},
			{SpanID: "b", ParentID: "a", Service: "checkout-api", Operation: "SELECT orders", Start: base, Duration: step(slowPG, i),
				Attrs: map[string]string{"net.peer.name": "postgres", "net.peer.port": "5432"}},
			{SpanID: "c", ParentID: "a", Service: "checkout-api", Operation: "GET cache", Start: base, Duration: step(slowRedis, i),
				Attrs: map[string]string{"net.peer.name": "redis", "net.peer.port": "6379"}},
		}})
	}
	return out
}

// twoDepFaults is the multi-fault schedule R-VER-3's fault-count rule can only
// call `ambiguous`: two dependencies were disturbed at once.
func twoDepFaults() []config.Fault {
	return []config.Fault{
		{Name: "pg_slow", Target: "postgres:5432", Verb: "latency", At: "peak", For: "15s"},
		{Name: "redis_slow", Target: "redis:6379", Verb: "latency", At: "peak", For: "15s"},
	}
}

// spec: R-VER-17
// spec: R-VER-14
func TestMultiFaultAttributedWhenExactlyOneTargetDegraded(t *testing.T) {
	withTraceSource(t, &fakeTraceSource{traces: twoDepTraces(true, false)})

	_, findings := evaluateThresholds(brokenThreshold(), twoDepFaults(), tracedSystem(), nil)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want 1", findings)
	}
	f := findings[0]
	if f.Cause == nil {
		t.Fatalf("cause = nil: exactly one fault target degraded in the ingested spans")
	}
	if f.Cause.Fault != "pg_slow" || f.Cause.Target != "postgres:5432" {
		t.Errorf("cause = %+v, want the fault whose target actually degraded (pg_slow/postgres:5432)", *f.Cause)
	}
	if len(f.Chain) == 0 || f.Chain[0].At != "postgres:5432" {
		t.Fatalf("chain = %+v, want hops from the degraded target", f.Chain)
	}
	if f.Confidence != verdict.Caused {
		t.Errorf("confidence = %q, want caused: a chain was built from real spans (R-VER-14)", f.Confidence)
	}
}

// spec: R-VER-17
func TestMultiFaultStaysAmbiguousWhenTwoTargetsDegraded(t *testing.T) {
	withTraceSource(t, &fakeTraceSource{traces: twoDepTraces(true, true)})

	_, findings := evaluateThresholds(brokenThreshold(), twoDepFaults(), tracedSystem(), nil)
	f := findings[0]
	if f.Cause != nil {
		t.Errorf("cause = %+v, want none: two dependencies degraded, so naming one would be a guess", *f.Cause)
	}
	if f.Chain != nil {
		t.Errorf("chain = %+v, want empty with no attributed cause", f.Chain)
	}
	if f.Confidence != verdict.Ambiguous {
		t.Errorf("confidence = %q, want ambiguous", f.Confidence)
	}
}

// spec: R-VER-17
func TestMultiFaultStaysAmbiguousWhenNoTargetDegraded(t *testing.T) {
	withTraceSource(t, &fakeTraceSource{traces: twoDepTraces(false, false)})

	_, findings := evaluateThresholds(brokenThreshold(), twoDepFaults(), tracedSystem(), nil)
	if findings[0].Cause != nil {
		t.Errorf("cause = %+v, want none: no candidate target degraded", *findings[0].Cause)
	}
	if findings[0].Confidence != verdict.Ambiguous {
		t.Errorf("confidence = %q, want ambiguous", findings[0].Confidence)
	}
}

// spec: R-VER-17
func TestMultiFaultStaysAmbiguousWithNoTraceBackend(t *testing.T) {
	withTraceSource(t, nil)

	_, findings := evaluateThresholds(brokenThreshold(), twoDepFaults(), tracedSystem(), nil)
	if findings[0].Cause != nil {
		t.Errorf("cause = %+v, want none: there are no traces to attribute from", *findings[0].Cause)
	}
	if findings[0].Confidence != verdict.Ambiguous {
		t.Errorf("confidence = %q, want ambiguous", findings[0].Confidence)
	}
}

// spec: R-VER-17
func TestMultiFaultStaysAmbiguousWhenTraceQueryFails(t *testing.T) {
	withTraceSource(t, &fakeTraceSource{err: errors.New("connection refused")})

	_, findings := evaluateThresholds(brokenThreshold(), twoDepFaults(), tracedSystem(), nil)
	if findings[0].Cause != nil {
		t.Errorf("cause = %+v, want none: the backend could not be read", *findings[0].Cause)
	}
	if findings[0].Confidence != verdict.Ambiguous {
		t.Errorf("confidence = %q, want ambiguous", findings[0].Confidence)
	}
}

// spec: R-VER-17
func TestTwoFaultsOnTheSameDegradedTargetStayAmbiguous(t *testing.T) {
	withTraceSource(t, &fakeTraceSource{traces: twoDepTraces(true, false)})

	faults := []config.Fault{
		{Name: "pg_slow", Target: "postgres:5432", Verb: "latency", At: "peak"},
		{Name: "pg_loss", Target: "postgres:5432", Verb: "packet_loss", At: "peak"},
	}
	_, findings := evaluateThresholds(brokenThreshold(), faults, tracedSystem(), nil)
	if findings[0].Cause != nil {
		t.Errorf("cause = %+v, want none: degradation cannot distinguish two faults on one target", *findings[0].Cause)
	}
	if findings[0].Confidence != verdict.Ambiguous {
		t.Errorf("confidence = %q, want ambiguous", findings[0].Confidence)
	}
}

// spec: R-VER-17
func TestNonAddressFaultTargetIsNotACandidate(t *testing.T) {
	withTraceSource(t, &fakeTraceSource{traces: twoDepTraces(true, false)})

	// A queue fault's target is a topic name, which no span attribute
	// names; only the postgres fault is a candidate, and with a single
	// candidate degrading, that is the attribution.
	faults := []config.Fault{
		{Name: "order_duplicate", Target: "orders", Verb: "duplicate", At: "peak"},
		{Name: "pg_slow", Target: "postgres:5432", Verb: "latency", At: "peak"},
	}
	_, findings := evaluateThresholds(brokenThreshold(), faults, tracedSystem(), nil)
	if findings[0].Cause == nil {
		t.Fatalf("cause = nil, want the only address-shaped candidate that degraded")
	}
	if findings[0].Cause.Fault != "pg_slow" {
		t.Errorf("cause.fault = %q, want pg_slow", findings[0].Cause.Fault)
	}
}

// spec: R-VER-13
func TestExplicitTraceEndpointIsQueriedThroughTheInReachTransport(t *testing.T) {
	// A Jaeger inside a DC-2-isolated compose stack has no published host
	// port, so the orchestrator must reach into the stack for it exactly
	// as it does for Prometheus and the broker.
	if _, ok := traceHTTPClient(true).Transport.(fallbackTransport); !ok {
		t.Errorf("explicit endpoint transport = %T, want fallbackTransport", traceHTTPClient(true).Transport)
	}
	if traceHTTPClient(false).Transport != nil {
		t.Errorf("guessed localhost endpoint transport = %T, want the plain default",
			traceHTTPClient(false).Transport)
	}
}
