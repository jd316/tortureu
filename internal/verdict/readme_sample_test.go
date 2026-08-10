package verdict

import (
	"os"
	"strings"
	"testing"
)

// spec: R-VER-9
//
// README's first code block is the first thing a visitor sees, and it claims
// to be "real output, not a mockup". That claim has been wrong three times in
// this project's history — each time because the renderer moved and the
// sample did not. Render the verdict it depicts and require every line of the
// block to come back, so the claim is enforced rather than repeated.
func TestREADMESampleIsRealRendererOutput(t *testing.T) {
	raw, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	// Locate the block by its content, not its position: the README's
	// ordering changes, and a positional split silently started reading a
	// different block when a ```sh fence was added above this one.
	const marker = "FAIL  checkout-spike"
	i := strings.Index(string(raw), marker)
	if i < 0 {
		t.Fatalf("README no longer contains the sample verdict (%q)", marker)
	}
	rest := string(raw)[i:]
	end := strings.Index(rest, "```")
	if end < 0 {
		t.Fatal("sample verdict block is not closed")
	}
	sample := rest[:end]

	got := Render(Verdict{
		Scenario: "checkout-spike", Status: StatusFail, DurationS: 280,
		Findings: []Finding{{
			Confidence: Correlated,
			Broke:      Broke{Assertion: "http_req_duration: p(95)<500", Observed: "4218ms"},
			Cause:      &Cause{Fault: "pg_slow", Target: "postgres:5432"},
			Candidates: []Candidate{{
				Library: "github.com/jackc/pgx/v5",
				Knobs:   []string{"MaxConns", "MinConns", "ConnConfig.ConnectTimeout"},
			}},
		}},
		Passed:      []Passed{{Assertion: "http_req_failed: rate<0.01", Observed: "0.003"}},
		EgressAudit: EgressAudit{Mocked: []string{"api.stripe.com"}, Blocked: []string{"telemetry.example"}},
	})

	for _, line := range strings.Split(strings.TrimSpace(sample), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(got, strings.TrimRight(line, " ")) {
			t.Errorf("README sample line is not produced by the renderer:\n  README: %q\n\nrendered:\n%s", line, got)
		}
	}
}
