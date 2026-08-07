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

// spec: R-EXE-19
//
// mock-url and broker-url must carry no invented default: NewRealDepsFull
// treats "" as "no applier wired" for error_rate (WireMock) and
// poison_pill/duplicate (broker), so a run declaring one of those faults
// with no address configured fails loudly instead of silently connecting
// to a guessed endpoint that happens to be someone else's WireMock. flag's
// own -h output is the only place a default is observable without a live
// orchestrator run: Go's flag.PrintDefaults omits the "(default ...)"
// annotation entirely for an empty-string default, so its absence here is
// the proof no address is hardcoded.
func TestRunApplierEndpointFlagsHaveNoHardcodedDefault(t *testing.T) {
	var out, errb bytes.Buffer
	code := runRun([]string{"-h"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (flag.ContinueOnError on -h)", code)
	}
	usage := errb.String()

	for _, name := range []string{"mock-url", "broker-url"} {
		flagDecl := "-" + name
		idx := strings.Index(usage, flagDecl)
		if idx == -1 {
			t.Fatalf("flag %s not declared:\n%s", flagDecl, usage)
		}
		// The block for one flag runs from its own "  -name" line up to
		// the next "\n  -" (the following flag) or end of string.
		block := usage[idx:]
		if next := strings.Index(block[1:], "\n  -"); next != -1 {
			block = block[:next+1]
		}
		if strings.Contains(block, "(default") {
			t.Errorf("flag %s has a hardcoded default:\n%s", flagDecl, block)
		}
	}
}

// spec: R-EXE-19
//
// This is the wiring proof itself, not just its absence-of-hardcoding: an
// empty -mock-url/-broker-url must leave the corresponding Deps field nil
// (so a run declaring error_rate/poison_pill/duplicate with no endpoint
// configured fails loudly per R-EXE-19), and a non-empty one must wire the
// real applier so the fault is not silently unreachable — the exact
// "built but unwired" gap this task exists to close one layer out from
// internal/run.
func TestBuildRealDepsWiresApplierEndpointsFromFlagValues(t *testing.T) {
	empty := buildRealDeps("", "", "", "")
	if empty.MockApplier != nil {
		t.Error("empty mock-url must leave Deps.MockApplier nil")
	}
	if empty.QueueApplier != nil {
		t.Error("empty broker-url must leave Deps.QueueApplier nil")
	}

	wired := buildRealDeps("", "", "http://localhost:8080", "http://localhost:9090")
	if wired.MockApplier == nil {
		t.Error("a non-empty mock-url must wire Deps.MockApplier, not leave it nil")
	}
	if wired.QueueApplier == nil {
		t.Error("a non-empty broker-url must wire Deps.QueueApplier, not leave it nil")
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
