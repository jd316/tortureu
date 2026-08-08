package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeVerdict(t *testing.T, dir, name, commit, status string, p99 float64) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := `{
  "run_id": "` + name + `",
  "scenario": "checkout",
  "status": "` + status + `",
  "started_at": "2026-08-09T10:00:00Z",
  "duration_s": 120,
  "commit": "` + commit + `",
  "findings": [],
  "passed": [],
  "egress_audit": {"mocked":[],"blocked":[],"real":[],"unclassified":[]},
  "observability": {"traces":false,"metrics":false,"logs":false},
  "metrics": {"http_req_duration": {"p(99)": ` + strconv.FormatFloat(p99, 'f', -1, 64) + `}}
}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// spec: R-CLI-14
func TestTrend_RecordThenShowReportsTheDelta(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "trend.jsonl")
	a := writeVerdict(t, dir, "a.json", strings.Repeat("a", 40), "pass", 100)
	b := writeVerdict(t, dir, "b.json", strings.Repeat("b", 40), "pass", 140)

	var out, errb bytes.Buffer
	for _, p := range []string{a, b} {
		out.Reset()
		errb.Reset()
		if code := Main([]string{"trend", "record", "-store", store, p}, &out, &errb); code != 0 {
			t.Fatalf("record %s: exit %d, stderr %s", p, code, errb.String())
		}
	}
	out.Reset()
	errb.Reset()
	if code := Main([]string{"trend", "show", "-store", store}, &out, &errb); code != 0 {
		t.Fatalf("show: exit %d, stderr %s", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "http_req_duration.p(99)") {
		t.Errorf("show does not report the metric:\n%s", got)
	}
	if !strings.Contains(got, "+40") {
		t.Errorf("show does not report the delta between the two commits:\n%s", got)
	}
	// R-CLI-14: a regression is reported, not enforced.
	if code := Main([]string{"trend", "show", "-store", store}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Errorf("show exited %d on a regression; a threshold policy is not ours to pick", code)
	}
}

// spec: R-CLI-14
func TestTrend_RecordReadsStdin(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "trend.jsonl")
	p := writeVerdict(t, dir, "a.json", strings.Repeat("c", 40), "pass", 100)

	// Drive the real stdin the dispatch passes in, rather than an injected
	// seam: `tortureu run -json | tortureu trend record -` is the pipeline
	// this is meant to make work.
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()

	var out, errb bytes.Buffer
	if code := Main([]string{"trend", "record", "-store", store, "-"}, &out, &errb); code != 0 {
		t.Fatalf("record -: exit %d, stderr %s", code, errb.String())
	}
	body, err := os.ReadFile(store)
	if err != nil || !strings.Contains(string(body), "cccccccc") {
		t.Fatalf("stdin verdict was not recorded: %v %s", err, body)
	}
}

// spec: R-CLI-17
func TestTrend_EmptyAnchorIsRecordedWarnedAndExcluded(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "trend.jsonl")
	p := writeVerdict(t, dir, "a.json", "", "pass", 100)

	var out, errb bytes.Buffer
	if code := Main([]string{"trend", "record", "-store", store, p}, &out, &errb); code != 0 {
		t.Fatalf("an anchorless run is still a run: exit %d", code)
	}
	if !strings.Contains(errb.String(), "commit") {
		t.Errorf("record must say the run has no anchor; stderr was %q", errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := Main([]string{"trend", "show", "-store", store}, &out, &errb); code != 0 {
		t.Fatalf("show: exit %d", code)
	}
	if !strings.Contains(out.String(), "no commit anchor") {
		t.Errorf("show must report the excluded run:\n%s", out.String())
	}
}

// spec: R-CLI-15
func TestTrend_UnknownSubcommandAndMissingFileExitTwo(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Main([]string{"trend", "wat"}, &out, &errb); code != 2 {
		t.Errorf("unknown subcommand: exit %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "record") {
		t.Errorf("the error must list what trend supports; got %q", errb.String())
	}
	errb.Reset()
	if code := Main([]string{"trend", "record", "/nope/nope.json"}, &out, &errb); code != 2 {
		t.Errorf("missing verdict: exit %d, want 2", code)
	}
}
