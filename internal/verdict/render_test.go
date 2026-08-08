package verdict

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func sampleVerdict() Verdict {
	return Verdict{
		RunID:     "2026-08-08T14:22:01Z-checkout-spike-a3f",
		Scenario:  "checkout-spike",
		Status:    StatusFail,
		StartedAt: "2026-08-08T14:22:01Z",
		DurationS: 280,
		Commit:    "a3f19c2",
		Findings: []Finding{
			{
				ID:         "f1",
				Confidence: Caused,
				Broke: Broke{
					Assertion:  "http_req_duration: p(99)<1500",
					Observed:   "4218ms",
					At:         "peak+12s",
					SustainedS: 47,
				},
				Cause: &Cause{
					Fault:  "pg_slow",
					Target: "postgres:5432",
					Inject: map[string]any{"latency": "300ms"},
				},
				Chain: []ChainHop{
					{At: "postgres", Observed: "query latency 4ms -> 304ms"},
					{At: "checkout-api", Observed: "p99 210ms -> 4218ms"},
				},
				Candidates: []Candidate{
					{Library: "jackc/pgx", Source: "go.mod", Knobs: []string{"MaxConns", "ConnConfig.ConnectTimeout"}},
				},
				Amplification: "20-conn pool + 3x retry turned 300ms of dep latency into 4.2s of user latency.",
			},
		},
		Passed: []Passed{
			{Assertion: "http_req_failed: rate<0.01", Observed: "0.003"},
		},
		EgressAudit: EgressAudit{
			Mocked:  []string{"api.stripe.com", "api.twilio.com"},
			Blocked: []string{"telemetry.vendor.io"},
		},
	}
}

// spec: R-VER-9
// Human output MUST be rendered from the same verdict document as machine
// (JSON) output — there must not be a second code path. We prove this by
// showing Render is a pure function of the *Verdict document*: every piece of
// data that also appears in the JSON encoding of the same document shows up
// in the human text, and changing the document changes the rendering — there
// is no separately-maintained human-only source of truth.
func TestRender_IsDerivedFromSameDocumentAsJSON(t *testing.T) {
	v := sampleVerdict()

	jsonBytes, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var doc map[string]any
	json.Unmarshal(jsonBytes, &doc)

	human := Render(v)

	// Every human-readable fact must trace back to a field present in the
	// JSON encoding of the very same document.
	mustContain := []string{
		v.Scenario,
		v.Commit,
		v.Findings[0].Broke.Assertion,
		v.Findings[0].Broke.Observed,
		v.Findings[0].Cause.Fault,
		string(v.Findings[0].Confidence),
		v.Findings[0].Amplification,
		v.Passed[0].Assertion,
	}
	for _, want := range mustContain {
		if !strings.Contains(human, want) {
			t.Errorf("Render(v) missing %q from the document; render:\n%s", want, human)
		}
	}

	// Sanity: the fields we asserted on actually exist in the JSON doc too
	// (i.e. we are checking against the *same* document, not a fixture).
	findings, _ := doc["findings"].([]any)
	if len(findings) == 0 {
		t.Fatalf("sanity check failed: JSON document has no findings")
	}

	// Mutating the document changes the render: proof there's no independent
	// human-only rendering path holding stale/hardcoded text.
	v2 := v
	v2.Scenario = "other-scenario"
	human2 := Render(v2)
	if strings.Contains(human2, v.Scenario) {
		t.Errorf("Render did not reflect a changed document field (scenario)")
	}
	if !strings.Contains(human2, "other-scenario") {
		t.Errorf("Render did not pick up the new scenario name")
	}

	// The exit code shown to the human MUST match ExitCode(v) computed from
	// the very same document, per R-VER-7/8.
	wantExit := ExitCode(v)
	if !strings.Contains(human, "exit "+strconv.Itoa(wantExit)) {
		t.Errorf("Render(v) does not show the exit code %d derived from the document", wantExit)
	}
}

// spec: R-DC2-2
// An aborted run MUST name every unclassified host — that's the entire reason
// the run refused to start. Before this fix, Render dropped
// EgressAudit.Unclassified entirely, so a user got "exit 3" with no
// explanation. Prove every unclassified host name appears in the human
// output.
func TestRender_NamesEveryUnclassifiedHostOnAbort(t *testing.T) {
	v := Verdict{
		Scenario:  "api",
		Status:    StatusAborted,
		DurationS: 60,
		EgressAudit: EgressAudit{
			Unclassified: []string{"api.partner.com", "cdn.unknown-vendor.net"},
		},
	}

	human := Render(v)

	for _, host := range v.EgressAudit.Unclassified {
		if !strings.Contains(human, host) {
			t.Errorf("Render(v) does not name unclassified host %q; render:\n%s", host, human)
		}
	}
	if !strings.Contains(human, "exit 3") {
		t.Errorf("Render(v) does not show exit 3 for an aborted verdict; render:\n%s", human)
	}
}

// spec: R-VER-7
// Exit 3 (aborted) covers two distinct causes: unclassified egress, or a
// failed reset. Verdict.Reset was carried in the document but never rendered,
// so a reset-failure abort looked identical to (and as unexplained as) any
// other abort. Prove a non-clean reset value is visible in the human output.
func TestRender_ShowsFailedReset(t *testing.T) {
	v := Verdict{
		Scenario:  "api",
		Status:    StatusAborted,
		DurationS: 60,
		Reset:     "failed",
	}

	human := Render(v)

	if !strings.Contains(human, "failed") {
		t.Errorf("Render(v) does not surface the failed reset; render:\n%s", human)
	}

	// A clean reset must not be reported as if it were a problem.
	v2 := v
	v2.Reset = "clean"
	human2 := Render(v2)
	if strings.Contains(human2, "reset:") {
		t.Errorf("Render(v) reports reset:clean as if it needed explaining; render:\n%s", human2)
	}
}
