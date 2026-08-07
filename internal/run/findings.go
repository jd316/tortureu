package run

import (
	"fmt"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/verdict"
)

// confidenceFor implements as much of the R-VER-3 table as this package can
// honestly claim: `caused` requires traces spanning the fault window, and no
// trace-ingestion pipeline exists anywhere in the built packages yet (no
// package here parses spans or joins them to a fault window), so this
// package never emits `caused` — doing so would be inventing a capability
// SPEC.md requires evidence for for and reporting it as fact (R-PROC-2/4).
// That gap is escalated in the Task 7 report rather than papered over here.
//
// `correlated` requires exactly one fault active in the breach window; this
// package does not have per-metric breach timestamps from k6's aggregate
// end-of-run summary (R-VER-10 forbids parsing anything richer than that
// JSON), so "exactly one fault active in the breach window" is approximated
// as "exactly one fault was scheduled for the whole run" — the narrowest
// honest reading available from the data this package actually has.
// Anything else (zero faults, or more than one) is `ambiguous`, per the
// table's own "ambiguous requires >=2 candidate causes" — zero faults isn't
// literally >=2, but SPEC.md does not define a confidence for a breach with
// no candidate cause at all, so this defaults to the least confident label
// rather than fabricating a middle one. Also escalated.
func confidenceFor(activeFaults int) verdict.Confidence {
	if activeFaults == 1 {
		return verdict.Correlated
	}
	return verdict.Ambiguous
}

// evaluateThresholds reads k6's per-metric threshold results out of
// IngestSummary's metrics map (k6's own JSON shape: metrics[name].thresholds
// is {expr: {ok: bool}}) and turns each into a Passed or Finding entry
// (R-VER-3, R-VER-5). Metrics with no thresholds sub-object are not
// k6-threshold assertions (they're plain metrics, or promql/sql entries
// internal/k6 already passes over) and are skipped here.
func evaluateThresholds(metrics map[string]any, activeFaults int) ([]verdict.Passed, []verdict.Finding) {
	var passed []verdict.Passed
	var findings []verdict.Finding

	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	// Deterministic order for stable verdicts across runs with identical data.
	sortStrings(names)

	for _, name := range names {
		m, ok := metrics[name].(map[string]any)
		if !ok {
			continue
		}
		thresholds, ok := m["thresholds"].(map[string]any)
		if !ok {
			continue
		}
		exprs := make([]string, 0, len(thresholds))
		for expr := range thresholds {
			exprs = append(exprs, expr)
		}
		sortStrings(exprs)

		for _, expr := range exprs {
			result, _ := thresholds[expr].(map[string]any)
			ok, _ := result["ok"].(bool)
			assertion := fmt.Sprintf("%s: %s", name, expr)
			if ok {
				passed = append(passed, verdict.Passed{Assertion: assertion, Observed: "threshold held"})
				continue
			}
			findings = append(findings, verdict.Finding{
				ID:         fmt.Sprintf("f%d", len(findings)+1),
				Confidence: confidenceFor(activeFaults),
				Broke: verdict.Broke{
					Assertion: assertion,
					Observed:  "threshold breached",
				},
			})
		}
	}
	return passed, findings
}

// evaluatePromqlAsserts evaluates every promql: entry in assert: (R-CFG-17)
// — the signals k6 cannot observe. A nil querier means no Prometheus
// endpoint was configured; such entries cannot be evaluated and are skipped
// rather than silently treated as passing (an unrun assertion must not look
// like a held one, R-VER-5).
func evaluatePromqlAsserts(asserts []config.AssertEntry, querier PromQuerier, activeFaults int) ([]verdict.Passed, []verdict.Finding) {
	if querier == nil {
		return nil, nil
	}
	var passed []verdict.Passed
	var findings []verdict.Finding
	for _, entry := range asserts {
		expr, ok := entry["promql"].(string)
		if !ok {
			continue
		}
		holds, observed, err := querier.Query(expr)
		assertion := "promql: " + expr
		if err != nil {
			findings = append(findings, verdict.Finding{
				ID:         fmt.Sprintf("f%d", len(findings)+1),
				Confidence: verdict.Ambiguous,
				Broke:      verdict.Broke{Assertion: assertion, Observed: "query error: " + err.Error()},
			})
			continue
		}
		if holds {
			passed = append(passed, verdict.Passed{Assertion: assertion, Observed: observed})
			continue
		}
		findings = append(findings, verdict.Finding{
			ID:         fmt.Sprintf("f%d", len(findings)+1),
			Confidence: confidenceFor(activeFaults),
			Broke:      verdict.Broke{Assertion: assertion, Observed: observed},
		})
	}
	return passed, findings
}

// sortStrings avoids importing sort at every call site for what is always a
// small slice.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
