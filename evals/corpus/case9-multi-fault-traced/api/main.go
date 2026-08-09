// checkout-api is the case-9 E1 fixture's SUT, and the corpus's first
// OpenTelemetry-instrumented one.
//
// The planted defect is the same family as case 1 — no timeout on the
// hot-path dependency — but the case exists to exercise something else:
// TWO faults are active at once (dep-a slowed, dep-b taken down), so
// R-VER-3's fault-count rule alone can only ever say `ambiguous`. Only the
// spans say which dependency the request path actually went bad at, which
// is what R-VER-17 attributes from.
//
// dep-b's outage is handled: a 200ms client timeout, and the response is
// served without it. dep-a's latency is not handled at all, so it is the
// dependency that actually breaks the SLO — and the one the verdict must
// name.
package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// noTimeoutClient is the planted defect: no deadline of any kind, so a slow
// dep-a stalls every in-flight request.
var noTimeoutClient = &http.Client{}

// handledClient is what dep-b is called with — a real timeout, so dep-b
// going down costs 200ms and nothing else.
var handledClient = &http.Client{Timeout: 200 * time.Millisecond}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// call makes one dependency request inside its own client span. The span
// names the peer with the current OTel semantic conventions
// (server.address / server.port), which is exactly what internal/trace
// matches a fault's "host:port" target against.
func call(ctx context.Context, tracer trace.Tracer, client *http.Client, name, url, host, port string) error {
	ctx, span := tracer.Start(ctx, "GET "+name, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("server.address", host),
			attribute.String("server.port", port),
		))
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		span.RecordError(err)
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func main() {
	ctx := context.Background()
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(env("OTEL_ENDPOINT", "http://jaeger:4318/v1/traces")),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		log.Fatalf("otlp exporter: %v", err)
	}
	tp := sdktrace.NewTracerProvider(
		// A short batch timeout so spans reach the backend during the run
		// rather than after it: TortureU queries the backend as soon as
		// load finishes.
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(time.Second)),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(env("OTEL_SERVICE_NAME", "checkout-api")),
		)),
	)
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("checkout-api")

	http.HandleFunc("/checkout", func(w http.ResponseWriter, r *http.Request) {
		ctx, span := tracer.Start(r.Context(), "POST /checkout", trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		// dep-b first, and its failure is tolerated: this is the fault
		// that must NOT be named as the cause.
		_ = call(ctx, tracer, handledClient, "dep-b", env("DEP_B_URL", "http://dep-b:9092/"), "dep-b", "9092")

		if err := call(ctx, tracer, noTimeoutClient, "dep-a", env("DEP_A_URL", "http://dep-a:9091/"), "dep-a", "9091"); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	log.Println("checkout-api listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
