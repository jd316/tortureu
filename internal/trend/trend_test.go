package trend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jdb316/tortureu/internal/verdict"
)

// v builds a verdict with the fields a trend record projects.
func v(commit, scenario string, status verdict.Status, p99 float64, findings ...verdict.Finding) verdict.Verdict {
	return verdict.Verdict{
		RunID:     "run-" + commit + "-" + scenario,
		Scenario:  scenario,
		Status:    status,
		StartedAt: "2026-08-09T10:00:00Z",
		DurationS: 120,
		Commit:    commit,
		Findings:  findings,
		Metrics: map[string]any{
			"http_req_duration": map[string]any{"p(99)": p99, "avg": 12.0},
			"http_req_failed":   0.01,
		},
	}
}

func finding(assertion, fault string) verdict.Finding {
	f := verdict.Finding{
		ID:         "f1",
		Confidence: verdict.Correlated,
		Broke:      verdict.Broke{Assertion: assertion, Observed: "x"},
	}
	if fault != "" {
		f.Cause = &verdict.Cause{Fault: fault, Target: "postgres:5432"}
	}
	return f
}

const sha1x = "1111111111111111111111111111111111111111"
const sha2x = "2222222222222222222222222222222222222222"
const sha3x = "3333333333333333333333333333333333333333"

// spec: R-CLI-15
func TestAppend_WritesOneJSONLinePerRecord(t *testing.T) {
	store := filepath.Join(t.TempDir(), "sub", "trend.jsonl")
	for _, c := range []string{sha1x, sha2x} {
		if err := Append(store, Project(v(c, "checkout", verdict.StatusPass, 100))); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	raw, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), raw)
	}
	for i, ln := range lines {
		var rec Record
		if err := json.Unmarshal([]byte(ln), &rec); err != nil {
			t.Fatalf("line %d is not JSON: %v", i+1, err)
		}
		if rec.V != SchemaVersion {
			t.Errorf("line %d: v = %d, want %d", i+1, rec.V, SchemaVersion)
		}
		if rec.Scenario != "checkout" || rec.Status != string(verdict.StatusPass) {
			t.Errorf("line %d: projection lost identity: %+v", i+1, rec)
		}
		// R-CLI-15: numeric leaves are flattened to dotted keys.
		if got := rec.Metrics["http_req_duration.p(99)"]; got != 100 {
			t.Errorf("line %d: p(99) = %v, want 100", i+1, got)
		}
		if _, ok := rec.Metrics["http_req_failed"]; !ok {
			t.Errorf("line %d: scalar metric leaf dropped: %+v", i+1, rec.Metrics)
		}
	}
}

// spec: R-CLI-15
func TestLoad_SkipsUnreadableLinesByNumberAndKeepsTheRest(t *testing.T) {
	store := filepath.Join(t.TempDir(), "trend.jsonl")
	good, _ := json.Marshal(Project(v(sha1x, "checkout", verdict.StatusPass, 100)))
	future := `{"v":9999,"run_id":"from-the-future","commit":"` + sha2x + `"}`
	good2, _ := json.Marshal(Project(v(sha3x, "checkout", verdict.StatusPass, 110)))
	body := string(good) + "\n" + "{not json\n" + future + "\n" + string(good2) + "\n"
	if err := os.WriteFile(store, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(store)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Records) != 2 {
		t.Fatalf("want the 2 readable records, got %d", len(s.Records))
	}
	if len(s.Skipped) != 2 {
		t.Fatalf("want 2 skip notes, got %+v", s.Skipped)
	}
	if s.Skipped[0].Line != 2 || s.Skipped[1].Line != 3 {
		t.Errorf("skip notes must name their line numbers, got %+v", s.Skipped)
	}
	if !strings.Contains(s.Skipped[1].Reason, "9999") {
		t.Errorf("unknown schema version must be named, got %q", s.Skipped[1].Reason)
	}
}

// spec: R-CLI-16
func TestAppend_ConcurrentWritersProduceWholeLines(t *testing.T) {
	store := filepath.Join(t.TempDir(), "trend.jsonl")
	const n = 32
	var wg sync.WaitGroup
	// A long scenario name makes an interleaved write long enough to be
	// visible: a torn record is the failure this guards.
	long := strings.Repeat("checkout-service-", 40)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := Project(v(sha1x, long, verdict.StatusPass, float64(i)))
			if err := Append(store, rec); err != nil {
				t.Errorf("Append: %v", err)
			}
		}(i)
	}
	wg.Wait()

	s, err := Load(store)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Skipped) != 0 {
		t.Errorf("concurrent appends tore a line: %+v", s.Skipped)
	}
	if len(s.Records) != n {
		t.Fatalf("want %d records, got %d", n, len(s.Records))
	}
}

// spec: R-CLI-17
func TestReport_EmptyAnchorIsKeptButNeverJoinsTheSeries(t *testing.T) {
	store := filepath.Join(t.TempDir(), "trend.jsonl")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(Append(store, Project(v(sha1x, "checkout", verdict.StatusPass, 100))))
	must(Append(store, Project(v("", "checkout", verdict.StatusPass, 9999)))) // not a git checkout
	must(Append(store, Project(v("", "checkout", verdict.StatusPass, 8888))))
	must(Append(store, Project(v(sha2x, "checkout", verdict.StatusPass, 130))))

	s, err := Load(store)
	if err != nil {
		t.Fatal(err)
	}
	rep := s.Report(Filter{})
	if len(rep.Rows) != 2 {
		t.Fatalf("want 2 anchored rows, got %d", len(rep.Rows))
	}
	if rep.Unanchored != 2 {
		t.Fatalf("want 2 unanchored records counted, got %d", rep.Unanchored)
	}
	// The delta must join sha1 -> sha2, never through the anchorless rows.
	got := rep.Rows[1].Deltas["http_req_duration.p(99)"]
	if got != 30 {
		t.Errorf("delta = %v, want 30 (130-100); an anchorless row corrupted the series", got)
	}
	out := Render(rep)
	if !strings.Contains(out, "no commit anchor") {
		t.Errorf("Render must report excluded rows and why; got:\n%s", out)
	}
}

// spec: R-CLI-17
func TestReport_ErrorRunIsShownButContributesNoMeasurement(t *testing.T) {
	store := filepath.Join(t.TempDir(), "trend.jsonl")
	_ = Append(store, Project(v(sha1x, "checkout", verdict.StatusPass, 100)))
	bad := v(sha2x, "checkout", verdict.StatusError, 0)
	bad.Error = "k6 not found on PATH"
	bad.Metrics = nil
	_ = Append(store, Project(bad))
	_ = Append(store, Project(v(sha3x, "checkout", verdict.StatusPass, 140)))

	s, _ := Load(store)
	rep := s.Report(Filter{})
	if len(rep.Rows) != 3 {
		t.Fatalf("the error run must still be shown, got %d rows", len(rep.Rows))
	}
	if rep.Rows[1].Comparable {
		t.Errorf("status=error carries no measurement of the SUT; it must not be comparable")
	}
	if len(rep.Rows[1].Deltas) != 0 {
		t.Errorf("status=error must contribute no delta, got %v", rep.Rows[1].Deltas)
	}
	// The next measured run compares against the last *measured* one.
	if got := rep.Rows[2].Deltas["http_req_duration.p(99)"]; got != 40 {
		t.Errorf("delta = %v, want 40 (140-100)", got)
	}
}

// spec: R-CLI-14
func TestReport_FindingsAreComparedByAssertionAndFaultNotByID(t *testing.T) {
	store := filepath.Join(t.TempDir(), "trend.jsonl")
	_ = Append(store, Project(v(sha1x, "checkout", verdict.StatusFail, 100,
		finding("http_req_duration: p(99)<1500", "pg_slow"))))
	// Same id "f1", different assertion+fault: a new finding, and the old one gone.
	_ = Append(store, Project(v(sha2x, "checkout", verdict.StatusFail, 120,
		finding("http_req_failed: rate<0.01", "redis_down"))))

	s, _ := Load(store)
	rep := s.Report(Filter{})
	row := rep.Rows[1]
	if len(row.NewFindings) != 1 || !strings.Contains(row.NewFindings[0], "http_req_failed") {
		t.Errorf("new finding not reported: %+v", row.NewFindings)
	}
	if len(row.GoneFindings) != 1 || !strings.Contains(row.GoneFindings[0], "pg_slow") {
		t.Errorf("resolved finding not reported: %+v", row.GoneFindings)
	}
	if !strings.Contains(Render(rep), "pg_slow") {
		t.Errorf("Render must name the finding that changed")
	}
}

// spec: R-CLI-14
func TestReport_ScenariosAreSeparateSeries(t *testing.T) {
	store := filepath.Join(t.TempDir(), "trend.jsonl")
	_ = Append(store, Project(v(sha1x, "checkout", verdict.StatusPass, 100)))
	_ = Append(store, Project(v(sha1x, "search", verdict.StatusPass, 500)))
	_ = Append(store, Project(v(sha2x, "checkout", verdict.StatusPass, 110)))

	s, _ := Load(store)
	rep := s.Report(Filter{})
	last := rep.Rows[len(rep.Rows)-1]
	if last.Scenario != "checkout" {
		t.Fatalf("unexpected last row %+v", last)
	}
	if got := last.Deltas["http_req_duration.p(99)"]; got != 10 {
		t.Errorf("delta = %v, want 10; the search run leaked into checkout's series", got)
	}
	// A scenario filter narrows the report.
	only := s.Report(Filter{Scenario: "search"})
	if len(only.Rows) != 1 {
		t.Errorf("Filter.Scenario ignored: %d rows", len(only.Rows))
	}
}

// spec: R-CLI-15
func TestLoad_MissingStoreIsAnEmptyTrendNotAnError(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("a store that does not exist yet is an empty trend: %v", err)
	}
	if len(s.Records) != 0 {
		t.Fatalf("want no records, got %d", len(s.Records))
	}
}
