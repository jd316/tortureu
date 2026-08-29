package mcp

import (
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/verdict"
)

func verdictWithFinding(f verdict.Finding) *verdict.Verdict {
	return &verdict.Verdict{
		RunID:    "run-1",
		Findings: []verdict.Finding{f},
	}
}

// spec: R-VER-4
func TestExplainFailure_ReportsCandidateLibraryAndKnobsNotFileLine(t *testing.T) {
	f := verdict.Finding{
		ID:         "f1",
		Confidence: verdict.Correlated,
		Broke:      verdict.Broke{Assertion: "http_req_duration p99 < 1.5s", Observed: "p99 4.2s"},
		Cause:      &verdict.Cause{Fault: "pg_slow", Target: "postgres"},
	}
	v := verdictWithFinding(f)
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "postgres", Type: "postgresql", Address: "postgres:5432", Clients: []string{"jackc/pgx"}},
		},
	}

	ex, err := ExplainFailure(v, "f1", sys)
	if err != nil {
		t.Fatalf("ExplainFailure: %v", err)
	}
	if ex.Fault != "pg_slow" {
		t.Errorf("Fault = %q, want pg_slow", ex.Fault)
	}
	if ex.Symptom.Observed != "p99 4.2s" {
		t.Errorf("Symptom.Observed = %q, want the finding's Broke.Observed carried through", ex.Symptom.Observed)
	}
	if len(ex.Candidates) == 0 {
		t.Fatal("Candidates is empty, want at least one candidate config surface (R-VER-4)")
	}
	for _, c := range ex.Candidates {
		if c.Library != "jackc/pgx" {
			t.Errorf("Candidate.Library = %q, want jackc/pgx", c.Library)
		}
		if len(c.Knobs) == 0 {
			t.Error("Candidate.Knobs is empty, want named knobs")
		}
		for _, knob := range c.Knobs {
			if strings.Contains(knob, ":") || strings.Contains(knob, "/") || strings.ContainsAny(knob, "0123456789") {
				t.Errorf("Candidate knob %q looks like a file:line, not a config knob — R-VER-4 forbids reporting a source location", knob)
			}
		}
		if strings.Contains(c.Source, "/") && strings.HasSuffix(c.Source, ".go") {
			t.Errorf("Candidate.Source %q looks like a source file path, not a provenance label — R-VER-4 forbids file:line", c.Source)
		}
	}
}

// spec: R-VER-4
func TestExplainFailure_NeverFabricatesCandidatesForAnUnmatchedTarget(t *testing.T) {
	f := verdict.Finding{
		ID:    "f1",
		Broke: verdict.Broke{Assertion: "x", Observed: "y"},
		Cause: &verdict.Cause{Fault: "unknown_fault", Target: "no-such-dependency"},
	}
	v := verdictWithFinding(f)
	sys := &detect.System{}

	ex, err := ExplainFailure(v, "f1", sys)
	if err != nil {
		t.Fatalf("ExplainFailure: %v", err)
	}
	if ex.Candidates == nil {
		t.Error("Candidates is nil for an unmatched target, want an explicit empty list rather than the zero value")
	}
	if len(ex.Candidates) != 0 {
		t.Errorf("Candidates = %+v, want none guessed for a target with no known dependency", ex.Candidates)
	}
}
