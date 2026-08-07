package run

import (
	"errors"
	"testing"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/verdict"
)

// spec: R-VER-5
func TestEvaluateThresholds_HeldThresholdIsListedAsPassed(t *testing.T) {
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(95)<500": map[string]any{"ok": true}},
		},
	}
	passed, findings := evaluateThresholds(metrics, 1)
	if len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findings)
	}
	if len(passed) != 1 || passed[0].Assertion != "http_req_duration: p(95)<500" {
		t.Errorf("passed = %v, want one entry naming the metric and expr", passed)
	}
}

// spec: R-VER-3
func TestEvaluateThresholds_BrokenThresholdWithOneFaultIsCorrelated(t *testing.T) {
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(99)<1500": map[string]any{"ok": false}},
		},
	}
	_, findings := evaluateThresholds(metrics, 1)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one", findings)
	}
	if findings[0].Confidence != verdict.Correlated {
		t.Errorf("Confidence = %q, want correlated when exactly one fault was active", findings[0].Confidence)
	}
}

// spec: R-VER-3
func TestEvaluateThresholds_BrokenThresholdWithMultipleFaultsIsAmbiguous(t *testing.T) {
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(99)<1500": map[string]any{"ok": false}},
		},
	}
	_, findings := evaluateThresholds(metrics, 2)
	if findings[0].Confidence != verdict.Ambiguous {
		t.Errorf("Confidence = %q, want ambiguous with >=2 candidate faults and no traces", findings[0].Confidence)
	}
}

// spec: R-VER-3
func TestEvaluateThresholds_NeverReportsCausedWithoutATracePipeline(t *testing.T) {
	// caused requires traces spanning the fault window (R-VER-3's table); no
	// trace-ingestion pipeline exists anywhere in the built packages, so
	// this package must never emit `caused` regardless of fault count.
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(99)<1500": map[string]any{"ok": false}},
		},
	}
	_, findings := evaluateThresholds(metrics, 1)
	if findings[0].Confidence == verdict.Caused {
		t.Error("Confidence = caused, but no trace pipeline exists to justify that claim")
	}
}

// spec: R-CFG-17
func TestEvaluatePromqlAsserts_SkipsUnrunAssertionsWithNoQuerierRatherThanPassingThem(t *testing.T) {
	asserts := []config.AssertEntry{{"promql": "up == 1"}}
	passed, findings := evaluatePromqlAsserts(asserts, nil, 1)
	if len(passed) != 0 || len(findings) != 0 {
		t.Errorf("passed=%v findings=%v, want both empty — an unrun assertion must not look like a held one (R-VER-5)", passed, findings)
	}
}

type fakeQuerier struct {
	holds    bool
	observed string
	err      error
}

func (f fakeQuerier) Query(expr string) (bool, string, error) { return f.holds, f.observed, f.err }

// spec: R-CFG-17
func TestEvaluatePromqlAsserts_FailedQueryIsAFinding(t *testing.T) {
	asserts := []config.AssertEntry{{"promql": "orders_total == payments_total"}}
	_, findings := evaluatePromqlAsserts(asserts, fakeQuerier{holds: false, observed: "no results"}, 1)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want one", findings)
	}
	if findings[0].Broke.Assertion != "promql: orders_total == payments_total" {
		t.Errorf("Broke.Assertion = %q", findings[0].Broke.Assertion)
	}
}

// spec: R-CFG-17
func TestEvaluatePromqlAsserts_QueryErrorIsAmbiguousFinding(t *testing.T) {
	asserts := []config.AssertEntry{{"promql": "invalid{"}}
	_, findings := evaluatePromqlAsserts(asserts, fakeQuerier{err: errors.New("bad query")}, 1)
	if len(findings) != 1 || findings[0].Confidence != verdict.Ambiguous {
		t.Fatalf("findings = %v, want one ambiguous finding for a query error", findings)
	}
}

// spec: R-EXE-4
func TestThroughputWarning_FlagsMoreThanFivePercentShortfall(t *testing.T) {
	cfg := &config.Config{Load: config.Load{Stages: []config.Stage{{Phase: "peak", Hold: "500rps", For: "60s"}}}}
	metrics := map[string]any{"http_reqs": map[string]any{"values": map[string]any{"rate": 400.0}}}
	warning, ok := throughputWarning(cfg, metrics)
	if !ok || warning == "" {
		t.Fatal("throughputWarning did not fire for a 20% shortfall (R-EXE-4: >5% trailing must warn)")
	}
}

// spec: R-EXE-4
func TestThroughputWarning_SilentWithinFivePercent(t *testing.T) {
	cfg := &config.Config{Load: config.Load{Stages: []config.Stage{{Phase: "peak", Hold: "500rps", For: "60s"}}}}
	metrics := map[string]any{"http_reqs": map[string]any{"values": map[string]any{"rate": 490.0}}}
	if _, ok := throughputWarning(cfg, metrics); ok {
		t.Error("throughputWarning fired within 5% of target, want silence")
	}
}
