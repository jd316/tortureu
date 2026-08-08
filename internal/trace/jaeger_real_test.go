// jaeger_real_test.go exercises ingestion against a *live* trace backend,
// not a fixture: Open must identify it, Traces must read spans out of it
// over the wire, and BuildChain must produce a hop list from those spans.
// The fixture test proves the parser against a recorded response; this one
// proves the whole path still works against a server that is running.
//
// It skips unless TORTUREU_TRACE_URL points at a reachable backend, because
// a developer machine is not required to have one. Reproduce with:
//
//	docker run -d -p 16686:16686 -p 4318:4318 jaegertracing/jaeger:2.10.0
//	# export ~40 OTLP traces through checkout-api -> postgres:5432
//	TORTUREU_TRACE_URL=http://localhost:16686 \
//	  TORTUREU_TRACE_SERVICE=checkout-api go test ./internal/trace/ -run Real -v
package trace

import (
	"context"
	"os"
	"testing"
	"time"
)

// spec: R-VER-13
func TestRealBackendEndToEnd(t *testing.T) {
	url := os.Getenv("TORTUREU_TRACE_URL")
	if url == "" {
		t.Skip("TORTUREU_TRACE_URL not set: no live trace backend to query")
	}
	service := os.Getenv("TORTUREU_TRACE_SERVICE")
	if service == "" {
		t.Skip("TORTUREU_TRACE_SERVICE not set: nothing to scope the query to")
	}
	target := os.Getenv("TORTUREU_TRACE_TARGET")
	if target == "" {
		t.Skip("TORTUREU_TRACE_TARGET not set: no fault target to chain from")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	src, err := Open(ctx, url, nil)
	if err != nil {
		t.Fatalf("Open(%s): %v", url, err)
	}
	traces, err := src.Traces(ctx, service, time.Now().Add(-time.Hour), time.Now(), 200)
	if err != nil {
		t.Fatalf("Traces: %v", err)
	}
	if len(traces) == 0 {
		t.Fatalf("no traces for service %q in the last hour", service)
	}
	t.Logf("read %d traces from %s", len(traces), url)

	hops := BuildChain(traces, target)
	if len(hops) == 0 {
		t.Fatalf("no chain built for target %q from %d live traces", target, len(traces))
	}
	for i, h := range hops {
		t.Logf("hop %d: at=%q observed=%q", i, h.At, h.Observed)
	}
	if hops[0].At != target {
		t.Errorf("first hop At = %q, want the fault target %q", hops[0].At, target)
	}
}
