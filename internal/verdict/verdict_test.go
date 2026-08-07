package verdict

import (
	"encoding/json"
	"strings"
	"testing"
)

// spec: R-VER-1
// Every run MUST emit one verdict document: it must serialize to JSON carrying
// the identifying fields that name the run.
func TestVerdict_EmitsDocumentWithIdentifyingFields(t *testing.T) {
	v := Verdict{
		RunID:     "2026-08-08T14:22:01Z-checkout-spike-a3f",
		Scenario:  "checkout-spike",
		Status:    StatusPass,
		StartedAt: "2026-08-08T14:22:01Z",
		DurationS: 280,
	}

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{"run_id", "scenario", "status", "started_at", "duration_s"} {
		if _, ok := m[key]; !ok {
			t.Errorf("verdict document missing required field %q", key)
		}
	}
}

// spec: R-VER-2
// status MUST be one of pass|fail|error|aborted, and `fail` (SUT broke an
// assertion) and `error` (TortureU itself broke) MUST NOT be conflated — they
// must serialize to distinct, stable string values.
func TestVerdict_StatusFailAndErrorAreDistinct(t *testing.T) {
	cases := []struct {
		status Status
		want   string
	}{
		{StatusPass, "pass"},
		{StatusFail, "fail"},
		{StatusError, "error"},
		{StatusAborted, "aborted"},
	}

	seen := map[string]bool{}
	for _, c := range cases {
		v := Verdict{Status: c.status}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		got, _ := m["status"].(string)
		if got != c.want {
			t.Errorf("Status %v marshaled to %q, want %q", c.status, got, c.want)
		}
		if seen[got] {
			t.Errorf("status value %q reused across distinct statuses", got)
		}
		seen[got] = true
	}

	if seen["fail"] == seen["error"] && len(seen) < 4 {
		t.Fatalf("fail and error statuses conflated")
	}
}

// spec: R-VER-3
// Each finding MUST carry a confidence of caused|correlated|ambiguous, assigned
// per-finding (not per-run): two findings on the same verdict must be able to
// carry independently different confidence values.
func TestFinding_ConfidenceIsPerFinding(t *testing.T) {
	v := Verdict{
		Status: StatusFail,
		Findings: []Finding{
			{ID: "f1", Confidence: Caused},
			{ID: "f2", Confidence: Correlated},
			{ID: "f3", Confidence: Ambiguous},
		},
	}

	b, _ := json.Marshal(v)
	var m map[string]any
	json.Unmarshal(b, &m)
	findings := m["findings"].([]any)
	want := []string{"caused", "correlated", "ambiguous"}
	for i, w := range want {
		f := findings[i].(map[string]any)
		if got := f["confidence"]; got != w {
			t.Errorf("finding %d confidence = %v, want %v", i, got, w)
		}
	}
}

// spec: R-VER-4
// Findings MUST report a candidate config surface (library + knobs), and MUST
// NOT report a file:line. The Candidate type has no field for a source
// location, and a real candidate's JSON must not carry file/line keys.
func TestFinding_CandidatesHaveNoFileLine(t *testing.T) {
	f := Finding{
		Candidates: []Candidate{
			{Library: "jackc/pgx", Source: "go.mod", Knobs: []string{"MaxConns", "ConnConfig.ConnectTimeout"}},
		},
	}
	b, _ := json.Marshal(f)
	s := strings.ToLower(string(b))
	for _, forbidden := range []string{`"file"`, `"line"`, `"file:line"`, `"filename"`} {
		if strings.Contains(s, forbidden) {
			t.Errorf("candidate JSON contains forbidden key %s: %s", forbidden, s)
		}
	}
	if len(f.Candidates[0].Knobs) == 0 || f.Candidates[0].Library == "" {
		t.Errorf("candidate must report library + knobs")
	}
}

// spec: R-VER-5
// Assertions that held MUST be listed (the `passed` list), so "it passed" is
// distinguishable from "it never ran" — and the list must render as an array
// even when empty, never null, so an agent can tell "no passes recorded" from
// "field absent/skipped".
func TestVerdict_PassedIsListedAndNeverNull(t *testing.T) {
	v := Verdict{
		Status: StatusPass,
		Passed: []Passed{
			{Assertion: "http_req_failed: rate<0.01", Observed: "0.003"},
		},
	}
	b, _ := json.Marshal(v)
	var m map[string]any
	json.Unmarshal(b, &m)
	passed, ok := m["passed"].([]any)
	if !ok {
		t.Fatalf("passed field missing or not an array")
	}
	if len(passed) != 1 {
		t.Fatalf("expected 1 passed entry, got %d", len(passed))
	}

	// Even with a nil slice, the document must render `[]`, not `null`.
	empty := Verdict{Status: StatusPass}
	b2, _ := json.Marshal(empty)
	if strings.Contains(string(b2), `"passed":null`) {
		t.Errorf("passed serialized as null, want empty array: %s", b2)
	}
}

// spec: R-VER-6
// The verdict MUST include an egress audit listing mocked, blocked, real and
// unclassified hosts.
func TestVerdict_EgressAuditListsAllFourCategories(t *testing.T) {
	v := Verdict{
		Status: StatusPass,
		EgressAudit: EgressAudit{
			Mocked:       []string{"api.stripe.com"},
			Blocked:      []string{"telemetry.vendor.io"},
			Real:         []string{},
			Unclassified: []string{},
		},
	}
	b, _ := json.Marshal(v)
	var m map[string]any
	json.Unmarshal(b, &m)
	audit, ok := m["egress_audit"].(map[string]any)
	if !ok {
		t.Fatalf("egress_audit missing")
	}
	for _, key := range []string{"mocked", "blocked", "real", "unclassified"} {
		if _, ok := audit[key]; !ok {
			t.Errorf("egress_audit missing %q", key)
		}
	}
}
