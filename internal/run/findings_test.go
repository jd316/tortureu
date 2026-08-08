package run

import (
	"errors"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/verdict"
)

// spec: R-VER-5
func TestEvaluateThresholds_HeldThresholdIsListedAsPassed(t *testing.T) {
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(95)<500": map[string]any{"ok": true}},
		},
	}
	passed, findings := evaluateThresholds(metrics, oneFault(), detect.System{})
	if len(findings) != 0 {
		t.Fatalf("findings = %v, want none", findings)
	}
	if len(passed) != 1 || passed[0].Assertion != "http_req_duration: p(95)<500" {
		t.Errorf("passed = %v, want one entry naming the metric and expr", passed)
	}
}

// spec: R-VER-1
func TestEvaluateThresholds_BrokenThresholdReportsMeasuredValue(t *testing.T) {
	// VERDICT.md §1 is normative for field names (SPEC.md §6) and its own
	// worked example shows "observed": "4218ms" — an actual measured
	// value, not a restatement of pass/fail. R-VER-1 ("every run MUST emit
	// one verdict document") is the umbrella requirement that document
	// conform to that normative schema; there is no more specific R-VER-n
	// for "observed must be a real value" than the schema itself.
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"contains": "time",
			"values":   map[string]any{"p(95)": 4218.0},
			"thresholds": map[string]any{
				"p(95)<500": map[string]any{"ok": false},
			},
		},
	}
	_, findings := evaluateThresholds(metrics, oneFault(), detect.System{})
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one", findings)
	}
	if got := findings[0].Broke.Observed; got != "4218ms" {
		t.Errorf("Broke.Observed = %q, want the measured p(95) value \"4218ms\" — a symbol (✗) already says it broke; Observed must say what happened", got)
	}
}

// spec: R-VER-1
func TestEvaluateThresholds_HeldThresholdReportsMeasuredValue(t *testing.T) {
	metrics := map[string]any{
		"http_req_failed": map[string]any{
			"values": map[string]any{"rate": 0.003},
			"thresholds": map[string]any{
				"rate<0.01": map[string]any{"ok": true},
			},
		},
	}
	passed, _ := evaluateThresholds(metrics, oneFault(), detect.System{})
	if len(passed) != 1 {
		t.Fatalf("passed = %v, want exactly one", passed)
	}
	if got := passed[0].Observed; got != "0.003" {
		t.Errorf("Observed = %q, want the measured rate value \"0.003\"", got)
	}
}

// spec: R-VER-1
func TestEvaluateThresholds_FallsBackToNotMeasuredWhenValueUnavailable(t *testing.T) {
	// The honesty rule: when this package genuinely cannot read the
	// measured value (no "values" object here), it must say so explicitly
	// rather than reuse text that implies a number was found.
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(95)<500": map[string]any{"ok": false}},
		},
	}
	_, findings := evaluateThresholds(metrics, oneFault(), detect.System{})
	if got := findings[0].Broke.Observed; got != "not measured" {
		t.Errorf("Broke.Observed = %q, want \"not measured\" — no values object was available to read a real number from", got)
	}
}

// spec: R-VER-1
func TestEvaluateThresholds_ReadsRealK6SummaryShapeNotNestedUnderValues(t *testing.T) {
	// Regression test: this exact shape is what real k6 --summary-export
	// actually produces (captured running a real k6 container against a
	// real SUT end to end for Task 7's R-DC2-3 load-path fix) — stats
	// directly on the metric object, no "values" wrapper, and a Rate-typed
	// metric's number under "value", not "rate". The original
	// implementation assumed a nested "values" shape that real k6 never
	// actually produces, so every threshold in a real run reported "not
	// measured" despite the number being right there in the summary.
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"avg": 0.522, "min": 0.461942, "med": 0.510763, "max": 0.572009,
			"p(90)": 0.571025, "p(95)": 0.5715169999999999,
			"contains":   "time",
			"thresholds": map[string]any{"p(95)<2000": map[string]any{"ok": true}},
		},
		"http_req_failed": map[string]any{
			"value": 0.0, "passes": 0.0, "fails": 5.0,
			"thresholds": map[string]any{"rate<0.5": map[string]any{"ok": true}},
		},
	}
	passed, findings := evaluateThresholds(metrics, oneFault(), detect.System{})
	if len(findings) != 0 {
		t.Fatalf("findings = %v, want none (both thresholds held)", findings)
	}
	if len(passed) != 2 {
		t.Fatalf("passed = %v, want two", passed)
	}
	byAssertion := map[string]string{}
	for _, p := range passed {
		byAssertion[p.Assertion] = p.Observed
	}
	if got := byAssertion["http_req_duration: p(95)<2000"]; got != "0.5715169999999999ms" {
		t.Errorf("p(95) observed = %q, want the real flat-shape value with ms appended", got)
	}
	if got := byAssertion["http_req_failed: rate<0.5"]; got != "0" {
		t.Errorf("rate observed = %q, want the real \"value\" field (0), not \"not measured\"", got)
	}
}

// spec: R-VER-8
func TestEvaluatePromqlAsserts_UnevaluatedAssertStillReadsUnevaluatedNotMeasured(t *testing.T) {
	// Regression guard: adding measured-value reporting must not make an
	// unevaluated assertion (no -prom-url configured) look like a measured
	// pass, which would undo the Critical fixed two rounds ago. A real
	// first run then showed *why* a string prefix in Broke.Observed wasn't
	// enough: it rendered as "p(95)<2000 -> 0.583" — a passing-looking
	// number next to a fail marker, worse than either a clear pass or fail.
	// internal/verdict now carries this structurally.
	asserts := []config.AssertEntry{{"promql": "up == 1"}}
	passed, findings := evaluatePromqlAsserts(asserts, nil, oneFault(), detect.System{})
	if len(passed) != 0 {
		t.Fatalf("passed = %v, want empty — an unevaluated assertion must never be listed as held", passed)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one", findings)
	}
	f := findings[0]
	if !f.Unevaluated {
		t.Error("Unevaluated = false, want true")
	}
	if !strings.Contains(f.Reason, "Prometheus") {
		t.Errorf("Reason = %q, want it to name the actual cause", f.Reason)
	}
	if strings.HasPrefix(f.Reason, "not evaluated") {
		t.Errorf("Reason = %q must not carry the \"not evaluated: \" prefix — the renderer supplies that now", f.Reason)
	}
	if f.Broke.Observed != "" {
		t.Errorf("Broke.Observed = %q, want empty — printing a value next to an unchecked assertion is what made the old output misleading", f.Broke.Observed)
	}
}

// oneFault/twoFaults are placeholder fault lists for tests that only care
// about the *count* (confidence), not attribution — see
// TestEvaluateThresholds_SingleActiveFaultRecordsItAsCause etc. for tests
// that care about the fault's actual content.
func oneFault() []config.Fault {
	return []config.Fault{{Name: "f1", Target: "svc:1", Verb: "latency", At: "peak"}}
}

func twoFaults() []config.Fault {
	return []config.Fault{
		{Name: "f1", Target: "svc:1", Verb: "latency", At: "peak"},
		{Name: "f2", Target: "svc:2", Verb: "down", At: "peak"},
	}
}

// spec: R-VER-3
func TestEvaluateThresholds_BrokenThresholdWithOneFaultIsCorrelated(t *testing.T) {
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(99)<1500": map[string]any{"ok": false}},
		},
	}
	_, findings := evaluateThresholds(metrics, oneFault(), detect.System{})
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
	_, findings := evaluateThresholds(metrics, twoFaults(), detect.System{})
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
	_, findings := evaluateThresholds(metrics, oneFault(), detect.System{})
	if findings[0].Confidence == verdict.Caused {
		t.Error("Confidence = caused, but no trace pipeline exists to justify that claim")
	}
}

// spec: R-VER-3
func TestEvaluateThresholds_SingleActiveFaultRecordsItAsCause(t *testing.T) {
	// D-4's confidence model is a claim about attribution; a finding marked
	// correlated with no Cause recorded doesn't say what it's attributing
	// to, which makes the confidence label meaningless.
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(99)<1500": map[string]any{"ok": false}},
		},
	}
	faults := []config.Fault{{
		Name: "pg_slow", At: "peak", For: "60s", Target: "postgres:5432",
		Verb: "latency", Inject: map[string]any{"latency": "300ms", "jitter": "50ms"},
	}}
	_, findings := evaluateThresholds(metrics, faults, detect.System{})
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one", findings)
	}
	cause := findings[0].Cause
	if cause == nil {
		t.Fatal("Cause is nil, want the single active fault recorded")
	}
	if cause.Fault != "pg_slow" {
		t.Errorf("Cause.Fault = %q, want pg_slow", cause.Fault)
	}
	if cause.Target != "postgres:5432" {
		t.Errorf("Cause.Target = %q, want postgres:5432", cause.Target)
	}
	if cause.Inject["latency"] != "300ms" {
		t.Errorf("Cause.Inject = %v, want the fault's own inject: block", cause.Inject)
	}
	if len(cause.Window) == 0 {
		t.Error("Cause.Window is empty, want the fault's phase anchor recorded")
	}
}

// spec: R-VER-3
func TestEvaluateThresholds_MultipleActiveFaultsRecordNoSingleCause(t *testing.T) {
	// Ambiguous means >=2 candidate causes — fabricating a single Cause here
	// would misreport an attribution this package cannot actually make.
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(99)<1500": map[string]any{"ok": false}},
		},
	}
	_, findings := evaluateThresholds(metrics, twoFaults(), detect.System{})
	if findings[0].Cause != nil {
		t.Errorf("Cause = %+v, want nil — ambiguous confidence must not fabricate a single attribution", findings[0].Cause)
	}
}

// spec: R-VER-4
func TestEvaluateThresholds_CandidatesComeFromTargetDetectedClients(t *testing.T) {
	// D-9: explain_failure looks up candidate config knobs from the causing
	// fault's target; with Candidates empty the MCP tool can only echo Broke
	// back, so this is what makes that feature do anything at all.
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(99)<1500": map[string]any{"ok": false}},
		},
	}
	faults := []config.Fault{{
		Name: "pg_slow", At: "peak", Target: "postgres:5432",
		Verb: "latency", Inject: map[string]any{"latency": "300ms"},
	}}
	deps := []detect.Dep{{
		Name: "postgres", Type: "postgresql", Address: "postgres:5432",
		Clients: []string{"github.com/jackc/pgx/v5"},
	}}
	_, findings := evaluateThresholds(metrics, faults, detect.System{Deps: deps})
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one", findings)
	}
	candidates := findings[0].Candidates
	if len(candidates) != 1 {
		t.Fatalf("Candidates = %v, want exactly one (the target's one detected client)", candidates)
	}
	c := candidates[0]
	if !strings.Contains(c.Library, "pgx") {
		t.Errorf("Candidate.Library = %q, want the detected pgx client", c.Library)
	}
	if len(c.Knobs) == 0 {
		t.Error("Candidate.Knobs is empty, want pgx's known pool-config knobs")
	}
	for _, k := range c.Knobs {
		// R-VER-4: a candidate is a library + knob names, never a file:line.
		if strings.ContainsAny(k, ":") || strings.Contains(k, ".go") {
			t.Errorf("Candidate.Knobs contains %q, which looks like a file:line, not a knob name (R-VER-4)", k)
		}
	}
}

// spec: R-VER-4
func TestEvaluateThresholds_UnknownClientLibraryGetsNoFabricatedKnobs(t *testing.T) {
	// The honesty rule: where this package cannot determine the actual
	// config surface, it must leave Knobs empty rather than guess one.
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(99)<1500": map[string]any{"ok": false}},
		},
	}
	faults := []config.Fault{{
		Name: "svc_slow", At: "peak", Target: "widget:1234",
		Verb: "latency", Inject: map[string]any{"latency": "300ms"},
	}}
	deps := []detect.Dep{{
		Name: "widget", Type: "widget", Address: "widget:1234",
		Clients: []string{"some-org/unheard-of-client"},
	}}
	_, findings := evaluateThresholds(metrics, faults, detect.System{Deps: deps})
	candidates := findings[0].Candidates
	if len(candidates) != 1 {
		t.Fatalf("Candidates = %v, want one entry naming the client even without known knobs", candidates)
	}
	if len(candidates[0].Knobs) != 0 {
		t.Errorf("Knobs = %v, want empty for a client library this package has no known knob table for", candidates[0].Knobs)
	}
}

// spec: R-VER-8
// spec: R-COV-6
func TestEvaluatePromqlAsserts_NoQuerierProducesUnevaluatedFindingNotSilentPass(t *testing.T) {
	// A previous version returned passed=nil, findings=nil here — the
	// assertion vanished entirely, which left Run's `len(Findings) == 0 =>
	// pass` check with nothing to see: a torture.yaml whose asserts are all
	// promql: with no -prom-url configured ran green having evaluated
	// nothing (R-VER-8's "a green that means we couldn't tell";
	// R-COV-6's "unevaluable must never read as false" — nor, by the same
	// reasoning, as true/pass). An unevaluated assertion must be visible as
	// its own thing: neither Passed nor a genuine broken Finding.
	asserts := []config.AssertEntry{{"promql": "up == 1"}}
	passed, findings := evaluatePromqlAsserts(asserts, nil, oneFault(), detect.System{})
	if len(passed) != 0 {
		t.Errorf("passed = %v, want empty — an unrun assertion must not look like a held one (R-VER-5)", passed)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one unevaluated finding", findings)
	}
	f := findings[0]
	if f.Confidence != verdict.Ambiguous {
		t.Errorf("Confidence = %q, want ambiguous — an unevaluated assertion is not a confident attribution of anything", f.Confidence)
	}
	if !f.Unevaluated {
		t.Error("Unevaluated = false, want true — the assertion was never evaluated, not broken")
	}
	if f.Broke.Observed != "" {
		t.Errorf("Broke.Observed = %q, want empty", f.Broke.Observed)
	}
}

type fakeQuerier struct {
	holds    bool
	observed string
	err      error
}

func (f fakeQuerier) Query(expr string) (bool, string, error) { return f.holds, f.observed, f.err }

// spec: R-VER-8
// spec: R-CFG-18
func TestEvaluateSQLAsserts_AlwaysUnevaluated(t *testing.T) {
	// sql: is parsed (R-CFG-18) but no package anywhere evaluates a SQL
	// assertion — the honest report is "not evaluated", every time, not
	// silence (which would let a sql:-only assert: block run green having
	// checked nothing, R-VER-8) and not a fabricated pass or fail.
	asserts := []config.AssertEntry{{"sql": "select count(*) from orders where status = 'orphaned'"}}
	findings := evaluateSQLAsserts(asserts)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one", findings)
	}
	f := findings[0]
	if f.Confidence != verdict.Ambiguous {
		t.Errorf("Confidence = %q, want ambiguous", f.Confidence)
	}
	if !f.Unevaluated {
		t.Error("Unevaluated = false, want true")
	}
	if f.Reason == "" {
		t.Error("Reason is empty, want it to say why sql: cannot be evaluated")
	}
	if strings.HasPrefix(f.Reason, "not evaluated") {
		t.Errorf("Reason = %q must not carry the \"not evaluated: \" prefix — the renderer supplies that now", f.Reason)
	}
	if f.Broke.Observed != "" {
		t.Errorf("Broke.Observed = %q, want empty", f.Broke.Observed)
	}
	if !strings.HasPrefix(f.Broke.Assertion, "sql:") {
		t.Errorf("Broke.Assertion = %q, want it to name the sql: assertion", f.Broke.Assertion)
	}
}

// spec: R-CFG-17
func TestEvaluatePromqlAsserts_FailedQueryIsAFinding(t *testing.T) {
	asserts := []config.AssertEntry{{"promql": "orders_total == payments_total"}}
	_, findings := evaluatePromqlAsserts(asserts, fakeQuerier{holds: false, observed: "no results"}, oneFault(), detect.System{})
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
	_, findings := evaluatePromqlAsserts(asserts, fakeQuerier{err: errors.New("bad query")}, oneFault(), detect.System{})
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
