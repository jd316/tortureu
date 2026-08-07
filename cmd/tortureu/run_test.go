package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/verdict"
)

// spec: R-VER-7
func TestEmitVerdictExitCodeMatchesVerdictPackage(t *testing.T) {
	cases := []verdict.Verdict{
		{Status: verdict.StatusPass},
		{Status: verdict.StatusError},
		{Status: verdict.StatusAborted},
		{Status: verdict.StatusFail, Findings: []verdict.Finding{{Confidence: verdict.Correlated}}},
		{Status: verdict.StatusFail, Findings: []verdict.Finding{{Confidence: verdict.Ambiguous}}},
	}
	for _, v := range cases {
		v := v
		var buf bytes.Buffer
		got := emitVerdict(&v, false, &buf)
		want := verdict.ExitCode(v)
		if got != want {
			t.Errorf("status %s: exit = %d, want %d", v.Status, got, want)
		}
	}
}

// spec: R-VER-8
func TestEmitVerdictExitFourIsNeverSuccess(t *testing.T) {
	v := verdict.Verdict{Status: verdict.StatusFail, Findings: []verdict.Finding{{Confidence: verdict.Ambiguous}}}
	var buf bytes.Buffer
	code := emitVerdict(&v, false, &buf)
	if code != 4 {
		t.Fatalf("setup: exit = %d, want 4", code)
	}
}

// spec: R-VER-9
func TestEmitVerdictJSONAndHumanRenderSameDocument(t *testing.T) {
	v := verdict.Verdict{
		Status:   verdict.StatusFail,
		Scenario: "checkout",
		Findings: []verdict.Finding{{ID: "f1", Confidence: verdict.Correlated, Broke: verdict.Broke{Assertion: "p95<500", Observed: "612ms"}}},
	}

	var human bytes.Buffer
	emitVerdict(&v, false, &human)
	if human.String() != verdict.Render(v) {
		t.Errorf("human output diverged from verdict.Render — a second formatting path exists (R-VER-9): %q", human.String())
	}

	var js bytes.Buffer
	emitVerdict(&v, true, &js)
	var decoded verdict.Verdict
	if err := json.Unmarshal(js.Bytes(), &decoded); err != nil {
		t.Fatalf("json output did not decode: %v", err)
	}
	if decoded.Status != v.Status || decoded.Scenario != v.Scenario || len(decoded.Findings) != len(v.Findings) {
		t.Errorf("json output diverged from the source verdict document: %+v", decoded)
	}
}

// spec: R-CLI-1
func TestRunMissingConfigExitsTwo(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	code := runRun([]string{"-config", filepath.Join(dir, "nope.yaml")}, &out, &errb)
	if code != 2 {
		t.Errorf("exit = %d, want 2: %s", code, errb.String())
	}
}

// spec: R-CLI-1
func TestRunInvalidConfigExitsTwo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "torture.yaml")
	if err := os.WriteFile(path, []byte("version: 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := runRun([]string{"-config", path}, &out, &errb)
	if code != 2 {
		t.Errorf("exit = %d, want 2: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "target.compose") {
		t.Errorf("expected config parse error surfaced, got: %s", errb.String())
	}
}
