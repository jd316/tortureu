package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// spec: R-CLI-7
func TestCheck_UnknownSubcommandExitsTwo(t *testing.T) {
	var out, errb bytes.Buffer
	code := Main([]string{"check", "bogus"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if strings.Contains(errb.String(), "unknown verb") {
		t.Errorf("stderr = %q, want a check-subcommand error, not top-level unknown verb", errb.String())
	}
}

// spec: R-CLI-7
//
// A baseline is a git ref or a file path the caller supplies; tortureu must
// not guess one (the task brief is explicit about this), so no baseline is
// a hard error, not a silently-assumed default.
func TestCheckContracts_RequiresBaseline(t *testing.T) {
	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("services:\n  app:\n    build: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := runCheckContracts([]string{"-compose", composePath}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "baseline") {
		t.Errorf("stderr = %q, want mention of --baseline", errb.String())
	}
}

// spec: R-CLI-7
//
// No spec:openapi and no spec:proto detected: there is nothing for oasdiff
// or buf breaking to check, so this is a pass (exit 0), not an error — the
// same way doctor reports "uncovered domains: none" as information, not
// failure.
func TestCheckContracts_NothingDetectedIsPass(t *testing.T) {
	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("services:\n  app:\n    build: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := runCheckContracts([]string{"-compose", composePath, "-baseline", "main"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "nothing to check") {
		t.Errorf("stdout = %q, want a nothing-to-check message", out.String())
	}
}

// spec: R-CLI-7
//
// R-CLI-5's pattern applied to check: when a spec type IS detected but the
// delegate tool that checks it is absent, report the gap with an install
// hint rather than failing obscurely, and treat it as an error (exit 2) —
// tortureu could not do the check it was asked to do, which is a different
// thing from the check running and finding a breaking change (exit 1).
// PATH is stubbed to a directory with no binaries in it (the same
// technique doctor_test.go uses for k6/docker) so oasdiff's absence is
// genuine, not a mocked-away LookPath.
func TestCheckContracts_MissingToolReportsHintAndErrors(t *testing.T) {
	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("services:\n  app:\n    build: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openapi.yaml"), []byte("openapi: 3.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())

	var out, errb bytes.Buffer
	code := runCheckContracts([]string{"-compose", composePath, "-baseline", "main"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2: stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	combined := out.String() + errb.String()
	if !strings.Contains(combined, "oasdiff") {
		t.Errorf("output = %q, want mention of oasdiff", combined)
	}
	if !strings.Contains(combined, "install") {
		t.Errorf("output = %q, want an install hint", combined)
	}
}

// spec: R-CLI-1
//
// check graduated from stub (main.go's stubVerbs) to a real verb; Main
// must route it to runCheck instead of the generic "not implemented"
// branch.
func TestCheck_DispatchedNotStubbed(t *testing.T) {
	var out, errb bytes.Buffer
	code := Main([]string{"check"}, &out, &errb)
	if strings.Contains(errb.String(), "not implemented in v0") {
		t.Errorf("check still dispatches to the stub branch: %s", errb.String())
	}
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (missing \"contracts\" subcommand)", code)
	}
}
