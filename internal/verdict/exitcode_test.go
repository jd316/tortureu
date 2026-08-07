package verdict

import "testing"

// spec: R-VER-7
// Exit codes: 0 pass, 1 fail (assertion broke), 2 error (TortureU/adapter
// failed), 3 aborted (unclassified egress or reset failed).
func TestExitCode_Table(t *testing.T) {
	cases := []struct {
		name string
		v    Verdict
		want int
	}{
		{"pass", Verdict{Status: StatusPass}, 0},
		{"fail", Verdict{Status: StatusFail, Findings: []Finding{{ID: "f1", Confidence: Caused}}}, 1},
		{"error", Verdict{Status: StatusError}, 2},
		{"aborted", Verdict{Status: StatusAborted}, 3},
	}
	for _, c := range cases {
		if got := ExitCode(c.v); got != c.want {
			t.Errorf("%s: ExitCode = %d, want %d", c.name, got, c.want)
		}
	}
}

// spec: R-VER-8
// Exit code 4 (inconclusive) MUST NOT be treated as success: a run that
// completed (status=fail, i.e. "ran clean") but whose findings are all
// `ambiguous` gets exit 4, distinct from both 0 (pass) and 1 (fail).
func TestExitCode_AllAmbiguousFindingsIsInconclusiveNotSuccess(t *testing.T) {
	v := Verdict{
		Status: StatusFail,
		Findings: []Finding{
			{ID: "f1", Confidence: Ambiguous},
			{ID: "f2", Confidence: Ambiguous},
		},
	}
	got := ExitCode(v)
	if got != 4 {
		t.Fatalf("all-ambiguous findings: ExitCode = %d, want 4", got)
	}
	if got == 0 {
		t.Fatalf("inconclusive (4) must never equal the pass code (0)")
	}

	// Mixing in a single non-ambiguous finding must NOT be inconclusive.
	v2 := Verdict{
		Status: StatusFail,
		Findings: []Finding{
			{ID: "f1", Confidence: Ambiguous},
			{ID: "f2", Confidence: Correlated},
		},
	}
	if got := ExitCode(v2); got != 1 {
		t.Fatalf("mixed confidence: ExitCode = %d, want 1 (fail)", got)
	}
}

// spec: R-VER-8
// SPEC.md now states the trigger as an algorithm: exit 4 requires status=fail
// AND every finding ambiguous. A run with NO findings is explicitly not
// inconclusive — it's a pass (0). This guards against "all findings are
// ambiguous" being (wrongly) computed as vacuously true over an empty slice,
// which would make a plain pass exit 4 instead of 0.
func TestExitCode_NoFindingsIsPassNeverInconclusive(t *testing.T) {
	v := Verdict{Status: StatusPass}
	if got := ExitCode(v); got != 0 {
		t.Fatalf("zero findings: ExitCode = %d, want 0 (pass), not 4", got)
	}
}
