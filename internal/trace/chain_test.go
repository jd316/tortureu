package trace

import (
	"strings"
	"testing"
	"time"
)

func realTraces(t *testing.T) []Trace {
	t.Helper()
	traces, err := decodeJaegerTraces(realJaegerResponse(t))
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return traces
}

// spec: R-VER-13
func TestBuildChainFromRealSpans(t *testing.T) {
	hops := BuildChain(realTraces(t), "postgres:5432")
	if len(hops) < 3 {
		t.Fatalf("got %d hops, want the full parent chain (>=3): %+v", len(hops), hops)
	}
	if hops[0].At != "postgres:5432" {
		t.Errorf("first hop At = %q, want the fault target (chain runs fault -> symptom)", hops[0].At)
	}
	// The fixture's db spans are 4ms before the fault and 304ms during it.
	if !strings.Contains(hops[0].Observed, "4ms -> 304ms") {
		t.Errorf("target hop Observed = %q, want the measured 4ms -> 304ms step", hops[0].Observed)
	}
	if !strings.Contains(hops[0].Observed, "n=40") {
		t.Errorf("target hop Observed = %q, want the span count it was measured over", hops[0].Observed)
	}
	last := hops[len(hops)-1]
	if !strings.HasPrefix(last.At, "gateway") {
		t.Errorf("last hop At = %q, want the trace root service (gateway)", last.At)
	}
	for i, h := range hops {
		if !strings.Contains(h.Observed, "->") {
			t.Errorf("hop %d Observed = %q: every hop must carry its own measured change", i, h.Observed)
		}
	}
}

// spec: R-VER-13
func TestBuildChainRefusesUnmatchedTarget(t *testing.T) {
	if hops := BuildChain(realTraces(t), "redis:6379"); hops != nil {
		t.Fatalf("got %+v, want no chain: no span in these traces touches redis:6379", hops)
	}
}

// spec: R-VER-13
func TestBuildChainRefusesWithoutObservedDegradation(t *testing.T) {
	base := time.Unix(1700000000, 0)
	var traces []Trace
	for i := 0; i < 20; i++ {
		// Every db span is 4ms: traces exist and the target is on the
		// request path, but the dependency never got slow.
		traces = append(traces, Trace{ID: "t", Spans: []Span{
			{SpanID: "a", Service: "checkout-api", Operation: "POST /checkout", Start: base, Duration: 20 * time.Millisecond},
			{SpanID: "b", ParentID: "a", Service: "checkout-api", Operation: "SELECT orders", Start: base, Duration: 4 * time.Millisecond,
				Attrs: map[string]string{"net.peer.name": "postgres", "net.peer.port": "5432"}},
		}})
	}
	if hops := BuildChain(traces, "postgres:5432"); hops != nil {
		t.Fatalf("got %+v, want no chain: no degradation was observed at the target", hops)
	}
}

// spec: R-VER-13
func TestBuildChainMatchesTargetByServiceName(t *testing.T) {
	base := time.Unix(1700000000, 0)
	var traces []Trace
	for i := 0; i < 20; i++ {
		d := 4 * time.Millisecond
		if i >= 10 {
			d = 400 * time.Millisecond
		}
		traces = append(traces, Trace{ID: "t", Spans: []Span{
			{SpanID: "a", Service: "checkout-api", Operation: "POST /checkout", Start: base, Duration: d + 10*time.Millisecond},
			// An instrumented dependency reports itself as its own service,
			// with no peer attributes at all.
			{SpanID: "b", ParentID: "a", Service: "postgres", Operation: "query", Start: base, Duration: d},
		}})
	}
	hops := BuildChain(traces, "postgres:5432")
	if len(hops) != 2 {
		t.Fatalf("got %d hops, want 2: %+v", len(hops), hops)
	}
	if hops[0].At != "postgres:5432" {
		t.Errorf("first hop At = %q, want postgres:5432", hops[0].At)
	}
}

// spec: R-VER-13
func TestBuildChainRefusesPortMismatch(t *testing.T) {
	base := time.Unix(1700000000, 0)
	var traces []Trace
	for i := 0; i < 20; i++ {
		d := 4 * time.Millisecond
		if i >= 10 {
			d = 400 * time.Millisecond
		}
		traces = append(traces, Trace{ID: "t", Spans: []Span{
			{SpanID: "a", Service: "checkout-api", Operation: "POST /checkout", Start: base, Duration: d},
			{SpanID: "b", ParentID: "a", Service: "checkout-api", Operation: "SELECT", Start: base, Duration: d,
				Attrs: map[string]string{"net.peer.name": "postgres", "net.peer.port": "5433"}},
		}})
	}
	if hops := BuildChain(traces, "postgres:5432"); hops != nil {
		t.Fatalf("got %+v, want no chain: the span names a different port on the same host", hops)
	}
}
