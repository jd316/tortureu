package contracts

import (
	"os"
	"path/filepath"
	"testing"
)

// spec: R-CLI-7
//
// When internal/detect's Coverage says a spec type is not present at all
// (R-COV-5 spec:openapi / spec:proto false), there is nothing to delegate
// to oasdiff or buf breaking — CheckOpenAPI/CheckProto must say so without
// touching PATH or running a command.
func TestCheckOpenAPI_NotApplicableWhenNotDetected(t *testing.T) {
	r := CheckOpenAPI(false, "main", "")
	if r.Outcome != OutcomeNotApplicable {
		t.Fatalf("Outcome = %v, want OutcomeNotApplicable", r.Outcome)
	}
}

func TestCheckProto_NotApplicableWhenNotDetected(t *testing.T) {
	r := CheckProto(false, "main", "")
	if r.Outcome != OutcomeNotApplicable {
		t.Fatalf("Outcome = %v, want OutcomeNotApplicable", r.Outcome)
	}
}

// spec: R-CLI-7
//
// This is the one path in this package a CI box without oasdiff/buf can
// prove for real: stub PATH to an empty directory (the same technique
// preflight_test.go uses for k6/docker) so exec.LookPath's failure is
// genuine, not mocked away, then assert the delegate tier reports the gap
// with an install hint instead of failing obscurely (R-CLI-5 pattern).
func TestCheckOpenAPI_MissingToolReportsHint(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := CheckOpenAPI(true, "main", "openapi.yaml")
	if r.Outcome != OutcomeMissingTool {
		t.Fatalf("Outcome = %v, want OutcomeMissingTool", r.Outcome)
	}
	if r.Hint == "" {
		t.Error("Hint is empty, want an install hint")
	}
}

func TestCheckProto_MissingToolReportsHint(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := CheckProto(true, "main", ".")
	if r.Outcome != OutcomeMissingTool {
		t.Fatalf("Outcome = %v, want OutcomeMissingTool", r.Outcome)
	}
	if r.Hint == "" {
		t.Error("Hint is empty, want an install hint")
	}
}

// spec: R-CLI-7
//
// FindOpenAPISpec locates the same conventional filenames R-COV-5 detects
// (openapi.yaml etc.) so `check contracts` can hand oasdiff a real path
// without re-implementing internal/detect's tree walk or importing its
// unexported filename table.
func TestFindOpenAPISpec_Found(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(want, []byte("openapi: 3.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := FindOpenAPISpec(dir)
	if err != nil {
		t.Fatalf("FindOpenAPISpec: %v", err)
	}
	if got != want {
		t.Errorf("FindOpenAPISpec = %q, want %q", got, want)
	}
}

func TestFindOpenAPISpec_NotFound(t *testing.T) {
	dir := t.TempDir()
	if _, err := FindOpenAPISpec(dir); err == nil {
		t.Error("FindOpenAPISpec: want error when no conventional filename exists")
	}
}
