package run

import (
	"errors"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/doctor"
	"github.com/jdb316/tortureu/internal/verdict"
)

// spec: R-VER-5
func TestEvaluateThresholds_HeldThresholdIsListedAsPassed(t *testing.T) {
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(95)<500": map[string]any{"ok": true}},
		},
	}
	passed, findings := evaluateThresholds(metrics, oneFault(), detect.System{}, nil)
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
	_, findings := evaluateThresholds(metrics, oneFault(), detect.System{}, nil)
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
	passed, _ := evaluateThresholds(metrics, oneFault(), detect.System{}, nil)
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
	_, findings := evaluateThresholds(metrics, oneFault(), detect.System{}, nil)
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
	passed, findings := evaluateThresholds(metrics, oneFault(), detect.System{}, nil)
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
	passed, findings := evaluatePromqlAsserts(asserts, nil, oneFault(), detect.System{}, nil)
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
	_, findings := evaluateThresholds(metrics, oneFault(), detect.System{}, nil)
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
	_, findings := evaluateThresholds(metrics, twoFaults(), detect.System{}, nil)
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
	_, findings := evaluateThresholds(metrics, oneFault(), detect.System{}, nil)
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
	_, findings := evaluateThresholds(metrics, faults, detect.System{}, nil)
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
	_, findings := evaluateThresholds(metrics, twoFaults(), detect.System{}, nil)
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
	_, findings := evaluateThresholds(metrics, faults, detect.System{Deps: deps}, nil)
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
	_, findings := evaluateThresholds(metrics, faults, detect.System{Deps: deps}, nil)
	candidates := findings[0].Candidates
	if len(candidates) != 1 {
		t.Fatalf("Candidates = %v, want one entry naming the client even without known knobs", candidates)
	}
	if len(candidates[0].Knobs) != 0 {
		t.Errorf("Knobs = %v, want empty for a client library this package has no known knob table for", candidates[0].Knobs)
	}
}

// spec: R-VER-4
//
// E1 found candidates missing on cases 1, 2 and 5 — the fault-driven cases,
// where attribution is most precise — because their detected client is
// Go's standard net/http, which had no entry in clientKnobPatterns at all.
// net/http is the most common HTTP client in Go by a wide margin, and case
// 1 is literally "HTTP client with no timeout" — the canonical resilience
// defect, and the case where naming the knob matters most. This proves the
// table now names its real, existing timeout and pool knobs: the
// whole-request deadline (Client.Timeout — the specific absence case 1
// plants), the finer-grained per-phase deadlines (Transport's
// ResponseHeaderTimeout, DialContext, TLSHandshakeTimeout), and the
// connection-pool knob (Transport.MaxIdleConnsPerHost — net/http's
// equivalent of case 3's pgx pool-exhaustion knob).
func TestEvaluateThresholds_NetHTTPClientCarriesTimeoutAndPoolKnobs(t *testing.T) {
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(99)<1500": map[string]any{"ok": false}},
		},
	}
	faults := []config.Fault{{
		Name: "dep_slow", At: "peak", Target: "dep:8080",
		Verb: "latency", Inject: map[string]any{"latency": "300ms"},
	}}
	deps := []detect.Dep{{
		Name: "dep", Type: "http", Address: "dep:8080",
		Clients: []string{"net/http"},
	}}
	_, findings := evaluateThresholds(metrics, faults, detect.System{Deps: deps}, nil)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one", findings)
	}
	candidates := findings[0].Candidates
	if len(candidates) != 1 {
		t.Fatalf("Candidates = %v, want exactly one (the target's one detected client)", candidates)
	}
	knobs := candidates[0].Knobs
	if len(knobs) == 0 {
		t.Fatal("Knobs is empty, want net/http's known timeout and pool knobs — this is the whole point of the fix")
	}
	wantSubstrings := []string{"Timeout", "MaxIdleConnsPerHost"}
	for _, want := range wantSubstrings {
		found := false
		for _, k := range knobs {
			if strings.Contains(k, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Knobs = %v, want an entry containing %q", knobs, want)
		}
	}
	// Case 1's own specific absence: the whole-request deadline.
	foundClientTimeout := false
	for _, k := range knobs {
		if strings.Contains(k, "Client.Timeout") {
			foundClientTimeout = true
		}
	}
	if !foundClientTimeout {
		t.Errorf("Knobs = %v, want Client.Timeout — the whole-request deadline case 1 plants the absence of", knobs)
	}
}

// spec: R-VER-4
//
// TBD-10, E1's final measurement: candidates 2/6. Case 1 — "HTTP client
// with no timeout," the corpus's canonical resilience defect — is detected
// and correctly attributed, and still could not name Client.Timeout as the
// fix, because net/http is stdlib and never appears in a go.mod require
// line: detect.Dep.Clients (R-DET-5, lockfile-only) structurally can never
// record it, no matter how complete clientKnobPatterns already is (the
// previous round's fix to that table was unreachable for exactly this
// reason).
//
// R-DET-1 forbids detection (and this package) from reading source to find
// this; R-AUD-5 explicitly permits internal/doctor's own bounded,
// table-driven construction-site inspection to do exactly that. This test
// proves the routing: with detect.Dep.Clients genuinely empty for the
// target (case 1's real shape — nothing a lockfile scan could ever find),
// an internal/doctor Finding naming net/http for the same dependency still
// reaches Candidates with Client.Timeout, sourced through
// candidatesFromAudit rather than any new source-reading this package does
// itself.
func TestEvaluateThresholds_AuditDiscoveredNetHTTPClientCarriesClientTimeout(t *testing.T) {
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(95)<1000": map[string]any{"ok": false}},
		},
	}
	faults := []config.Fault{{
		Name: "dep_slow", At: "peak", Target: "dep:9090",
		Verb: "latency", Inject: map[string]any{"latency": "3s"},
	}}
	// The real shape of case 1's "dep": detect never recorded a client at
	// all (net/http is stdlib, no go.mod entry can ever name it) — this is
	// deliberately NOT the same fixture as
	// TestEvaluateThresholds_NetHTTPClientCarriesTimeoutAndPoolKnobs above,
	// which simulated Clients already containing "net/http" (impossible in
	// reality) to prove the knob table alone. This test proves the other,
	// previously-missing half: getting "net/http" into Candidates at all
	// when Clients is empty.
	deps := []detect.Dep{{Name: "dep", Type: "http", Address: "dep:9090"}}
	auditFindings := []doctor.Finding{
		{DepName: "dep", DepType: "http", Library: "net/http", Check: doctor.CheckTimeout, Determined: true, Present: false},
	}

	_, findings := evaluateThresholds(metrics, faults, detect.System{Deps: deps}, auditFindings)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one", findings)
	}
	candidates := findings[0].Candidates
	if len(candidates) != 1 {
		t.Fatalf("Candidates = %v, want exactly one (net/http, sourced from the audit since detect.Dep.Clients is empty)", candidates)
	}
	if !strings.Contains(candidates[0].Library, "net/http") {
		t.Errorf("Candidate.Library = %q, want net/http", candidates[0].Library)
	}
	found := false
	for _, k := range candidates[0].Knobs {
		if strings.Contains(k, "Client.Timeout") {
			found = true
		}
	}
	if !found {
		t.Errorf("Knobs = %v, want Client.Timeout — case 1's own specific defect, and the single most valuable candidate result this tool can produce", candidates[0].Knobs)
	}
}

// spec: R-VER-4
//
// A dependency name the audit has no finding for must not fabricate a
// candidate, and an audit finding for a library outside clientKnobPatterns
// must still get a name and no knobs — the same honesty rules already
// enforced for lockfile-sourced candidates apply identically here.
func TestEvaluateThresholds_AuditFindingForUnknownLibraryGetsNoFabricatedKnobs(t *testing.T) {
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(95)<1000": map[string]any{"ok": false}},
		},
	}
	faults := []config.Fault{{
		Name: "widget_slow", At: "peak", Target: "widget:1234",
		Verb: "latency", Inject: map[string]any{"latency": "300ms"},
	}}
	deps := []detect.Dep{{Name: "widget", Type: "widget", Address: "widget:1234"}}
	auditFindings := []doctor.Finding{
		{DepName: "widget", DepType: "widget", Library: "some-org/unheard-of-client", Check: doctor.CheckTimeout, Determined: true, Present: false},
		// A finding for a different dependency must not leak in as a
		// candidate for this one.
		{DepName: "some-other-dep", DepType: "http", Library: "net/http", Check: doctor.CheckTimeout, Determined: true, Present: false},
	}

	_, findings := evaluateThresholds(metrics, faults, detect.System{Deps: deps}, auditFindings)
	candidates := findings[0].Candidates
	if len(candidates) != 1 {
		t.Fatalf("Candidates = %v, want exactly one (only widget's own finding, not the other dependency's)", candidates)
	}
	if candidates[0].Library != "some-org/unheard-of-client" {
		t.Errorf("Candidate.Library = %q, want the widget finding's own library", candidates[0].Library)
	}
	if len(candidates[0].Knobs) != 0 {
		t.Errorf("Knobs = %v, want empty for a library outside clientKnobPatterns", candidates[0].Knobs)
	}
}

// spec: R-VER-4
//
// E1 found candidates 0/6: attribute() only ever pulled a candidate's
// dependency from an active fault's target, so a finding with no causing
// fault never got candidates at all — even when the relevant client was
// detected perfectly. Two of E1's six real detections were load-only, no
// fault involved (a connection pool exhausted under load, a cache
// stampede on TTL expiry): exactly the cases D-9's candidate surface is
// most useful for (pool exhaustion points at MaxConns, a stampede points
// at TTL/singleflight settings), and the information never reached the
// user. This proves a fault-free finding now sources candidates from every
// client library detect.System actually found, not just the ones facing
// an active fault target.
func TestEvaluateThresholds_NoActiveFaultStillAttachesCandidatesFromDetectedClients(t *testing.T) {
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(95)<500": map[string]any{"ok": false}},
		},
	}
	deps := []detect.Dep{{
		Name: "postgres", Type: "postgresql", Address: "postgres:5432",
		Clients: []string{"github.com/jackc/pgx/v5"},
	}}
	_, findings := evaluateThresholds(metrics, nil, detect.System{Deps: deps}, nil)
	if len(findings) != 1 {
		t.Fatalf("findings = %v, want exactly one", findings)
	}
	f := findings[0]
	if f.Cause != nil {
		t.Errorf("Cause = %+v, want nil — no fault was active, so there is nothing to name as the cause", f.Cause)
	}
	candidates := f.Candidates
	if len(candidates) != 1 {
		t.Fatalf("Candidates = %v, want one entry for the one detected client, even with no active fault", candidates)
	}
	c := candidates[0]
	if !strings.Contains(c.Library, "pgx") {
		t.Errorf("Candidate.Library = %q, want the detected pgx client", c.Library)
	}
	if len(c.Knobs) == 0 {
		t.Error("Candidate.Knobs is empty, want pgx's known pool-config knobs — the table has this library, it must not be withheld just because no fault fired")
	}
	// Without a causing fault, this is the plausible set load alone
	// revealed matters for, not a diagnosis naming this client as the
	// culprit — the finding's own Confidence (ambiguous, since
	// confidenceFor(0) is not `correlated`) already signals that at the
	// finding level, but the Source label must also make it legible
	// looking at the candidate alone, since D-9's own schema has no
	// separate confidence-per-candidate field to carry it.
	if !strings.Contains(strings.ToLower(c.Source), "no active fault") {
		t.Errorf("Candidate.Source = %q, want it to say plainly this is not from an active fault — otherwise it reads identically to a fault-scoped, tightly-attributed candidate", c.Source)
	}
}

// spec: R-VER-4
func TestEvaluateThresholds_NoActiveFaultUnknownClientStillGetsNoFabricatedKnobs(t *testing.T) {
	// The honesty rule holds in the fault-free path too: an unrecognized
	// library gets named and nothing else, never a guessed knob.
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(95)<500": map[string]any{"ok": false}},
		},
	}
	deps := []detect.Dep{{
		Name: "widget", Type: "widget", Address: "widget:1234",
		Clients: []string{"some-org/unheard-of-client"},
	}}
	_, findings := evaluateThresholds(metrics, nil, detect.System{Deps: deps}, nil)
	candidates := findings[0].Candidates
	if len(candidates) != 1 {
		t.Fatalf("Candidates = %v, want one entry naming the client even without known knobs", candidates)
	}
	if len(candidates[0].Knobs) != 0 {
		t.Errorf("Knobs = %v, want empty for a client library this package has no known knob table for", candidates[0].Knobs)
	}
}

// spec: R-VER-4
func TestEvaluateThresholds_NoActiveFaultAndNoDetectedClientAttachesNothing(t *testing.T) {
	// Case 6's shape (an in-process unbounded queue): genuinely nobody's
	// library. Attaching nothing is correct; padding Candidates with
	// unrelated detected clients would misdirect the reader.
	metrics := map[string]any{
		"http_req_duration": map[string]any{
			"thresholds": map[string]any{"p(95)<500": map[string]any{"ok": false}},
		},
	}
	_, findings := evaluateThresholds(metrics, nil, detect.System{}, nil)
	if len(findings[0].Candidates) != 0 {
		t.Errorf("Candidates = %v, want empty — no dependency was detected at all, so there is nothing to point at", findings[0].Candidates)
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
	passed, findings := evaluatePromqlAsserts(asserts, nil, oneFault(), detect.System{}, nil)
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
	_, findings := evaluatePromqlAsserts(asserts, fakeQuerier{holds: false, observed: "no results"}, oneFault(), detect.System{}, nil)
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
	_, findings := evaluatePromqlAsserts(asserts, fakeQuerier{err: errors.New("bad query")}, oneFault(), detect.System{}, nil)
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
