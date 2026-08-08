// Package trace ingests distributed traces from a running trace backend so
// a finding can carry a real causal chain (R-VER-13) and, when one is
// built, the `caused` confidence D-4 reserves for evidence of the request
// path (R-VER-14).
//
// The rule this package exists to enforce is negative: it must never invent
// a hop. Every value it produces is read off a span a backend actually
// returned, and every path that cannot produce one — no backend, an
// unsupported backend, no span touching the fault target, no measurable
// degradation at it — returns nothing rather than something plausible.
// A fabricated causal story is the one field of a verdict a reader has no
// way to check, which is exactly why it is the one field that must not be
// guessed.
package trace

import (
	"context"
	"time"
)

// Span is one span, normalized away from any backend's wire shape.
// Service is the resolved service name (Jaeger keeps it in a per-trace
// `processes` map, not on the span). Attrs are the span's tags/attributes,
// values rendered as strings — they are what identifies which peer a client
// span talked to.
type Span struct {
	TraceID   string
	SpanID    string
	ParentID  string
	Service   string
	Operation string
	Start     time.Time
	Duration  time.Duration
	Attrs     map[string]string
}

// Trace is one trace: a set of spans that share a trace id.
type Trace struct {
	ID    string
	Spans []Span
}

// Hop is one fault -> symptom hop of VERDICT.md §1's `chain`. At names
// where it was observed; Observed is the measured change there. Both are
// derived from spans; neither is ever composed from the fault declaration.
type Hop struct {
	At       string
	Observed string
}

// Source is a queryable trace backend. service scopes the query to the
// system under test (a backend shared with unrelated systems must not
// contribute spans to this run's chain); start/end bound the window; limit
// caps how many traces are sampled.
type Source interface {
	Traces(ctx context.Context, service string, start, end time.Time, limit int) ([]Trace, error)
}
