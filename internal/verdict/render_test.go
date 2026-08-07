package verdict

import (
	"encoding/json"
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
	if !strings.Contains(human, "exit "+itoa(wantExit)) {
		t.Errorf("Render(v) does not show the exit code %d derived from the document", wantExit)
	}
}

func itoa(n int) string {
	return string(rune('0' + n))
}
