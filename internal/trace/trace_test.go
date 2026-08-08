package trace

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// realJaegerResponse is a byte-for-byte capture of a real
// jaegertracing/jaeger:2.10.0 answering
// GET /api/traces?service=checkout-api&start=&end=&limit=40 after 40 real
// OTLP traces were exported into it over :4318. It is checked in rather
// than hand-written so the parser is proved against the shape Jaeger
// actually emits (per-trace `processes` map, `references`, microsecond
// `startTime`/`duration`, typed `tags`), not against a shape we imagined.
func realJaegerResponse(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/jaeger_traces.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

// spec: R-VER-13
func TestJaegerParsesRealResponse(t *testing.T) {
	body := realJaegerResponse(t)
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	j := &Jaeger{BaseURL: srv.URL, HTTP: srv.Client()}
	start := time.Unix(1700000000, 0)
	end := start.Add(5 * time.Minute)
	traces, err := j.Traces(context.Background(), "checkout-api", start, end, 40)
	if err != nil {
		t.Fatalf("Traces: %v", err)
	}
	for _, want := range []string{"/api/traces", "service=checkout-api", "start=1700000000000000", "end=1700000300000000", "limit=40"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if len(traces) != 40 {
		t.Fatalf("got %d traces, want 40", len(traces))
	}

	var db *Span
	for i := range traces[0].Spans {
		if traces[0].Spans[i].Operation == "SELECT orders" {
			db = &traces[0].Spans[i]
		}
	}
	if db == nil {
		t.Fatal("no 'SELECT orders' span parsed from the real response")
	}
	if db.Service != "checkout-api" {
		t.Errorf("Service = %q, want checkout-api (resolved through the trace's processes map)", db.Service)
	}
	if db.ParentID == "" {
		t.Error("ParentID empty: CHILD_OF reference not parsed")
	}
	if db.Duration <= 0 {
		t.Error("Duration not parsed")
	}
	if db.Attrs["net.peer.name"] != "postgres" {
		t.Errorf("net.peer.name = %q, want postgres", db.Attrs["net.peer.name"])
	}
	if db.Attrs["net.peer.port"] != "5432" {
		t.Errorf("net.peer.port = %q, want 5432 (int64-typed tag)", db.Attrs["net.peer.port"])
	}
}

// spec: R-VER-13
func TestOpenIdentifiesJaeger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/services" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":["gateway","checkout-api"],"total":2,"limit":0,"offset":0,"errors":null}`))
	}))
	defer srv.Close()

	src, err := Open(context.Background(), srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, ok := src.(*Jaeger); !ok {
		t.Fatalf("got %T, want *Jaeger", src)
	}
}

// spec: R-VER-13
func TestOpenRefusesTempoByName(t *testing.T) {
	// Real grafana/tempo:2.9.0 behaviour: /api/echo answers "echo",
	// /api/services 404s.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/echo" {
			_, _ = w.Write([]byte("echo"))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	src, err := Open(context.Background(), srv.URL, srv.Client())
	if src != nil {
		t.Errorf("got a source %T for a Tempo endpoint, want none", src)
	}
	if err == nil {
		t.Fatal("Tempo endpoint accepted silently; want a refusal naming the backend")
	}
	msg := err.Error()
	if !strings.Contains(strings.ToLower(msg), "tempo") {
		t.Errorf("refusal %q does not name Tempo", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "jaeger") {
		t.Errorf("refusal %q does not say which backend is supported", msg)
	}
}

// spec: R-VER-13
func TestOpenRefusesUnidentifiedEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	if _, err := Open(context.Background(), srv.URL, srv.Client()); err == nil {
		t.Fatal("an endpoint that is neither Jaeger nor Tempo was accepted; want a refusal")
	}
}
