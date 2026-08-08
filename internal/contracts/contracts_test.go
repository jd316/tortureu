package contracts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// realTool skips a test unless the named delegate binary is on PATH. These
// tests are the only proof that R-VER-2's result/error distinction actually
// holds, because it depends entirely on exit codes this project does not
// choose — so they must run the real binary or not pretend to run at all.
func realTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH; this check requires the real binary", name)
	}
}

// spec: R-CLI-7
//
// A removed endpoint is a breaking change and MUST surface as OutcomeBreaking
// (R-VER-2: a result, exit 1), never OutcomePass. `oasdiff breaking` exits 0
// even when it reports errors — it only fails on --fail-on ERR — so a mapping
// that trusts the bare exit status reports a clean pass on a broken API. That
// is the false negative this verb exists to prevent.
func TestCheckOpenAPI_RemovedEndpointIsBreaking(t *testing.T) {
	realTool(t, OASDiff)
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yaml")
	rev := filepath.Join(dir, "openapi.yaml")
	const withOrders = `openapi: 3.0.0
info: {title: demo, version: 1.0.0}
paths:
  /health: {get: {responses: {'200': {description: ok}}}}
  /orders: {get: {responses: {'200': {description: ok}}}}
`
	const withoutOrders = `openapi: 3.0.0
info: {title: demo, version: 1.0.0}
paths:
  /health: {get: {responses: {'200': {description: ok}}}}
`
	if err := os.WriteFile(base, []byte(withOrders), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rev, []byte(withoutOrders), 0o644); err != nil {
		t.Fatal(err)
	}

	r := CheckOpenAPI(true, base, rev)
	if r.Outcome != OutcomeBreaking {
		t.Fatalf("Outcome = %v, want OutcomeBreaking\noutput:\n%s\nerr: %v", r.Outcome, r.Output, r.Err)
	}

	// The other half of the same distinction: an unchanged spec must pass,
	// or "breaking" would just mean "oasdiff ran".
	if r := CheckOpenAPI(true, base, base); r.Outcome != OutcomePass {
		t.Fatalf("identical specs: Outcome = %v, want OutcomePass\noutput:\n%s", r.Outcome, r.Output)
	}
}

// spec: R-CLI-7
//
// `buf breaking` exits 100 on a breaking change, not 1. Mapping only 1 sends
// a real finding down the OutcomeError path, which R-VER-2 forbids: it would
// tell a user to debug TortureU instead of their schema.
func TestCheckProto_RemovedFieldIsBreaking(t *testing.T) {
	realTool(t, Buf)
	dir := t.TempDir()
	base := filepath.Join(dir, "base")
	cur := filepath.Join(dir, "cur")
	for path, proto := range map[string]string{
		base: "syntax = \"proto3\";\npackage demo;\nmessage Order { string id = 1; int32 qty = 2; }\n",
		cur:  "syntax = \"proto3\";\npackage demo;\nmessage Order { string id = 1; }\n",
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "a.proto"), []byte(proto), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "buf.yaml"), []byte("version: v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if r := CheckProto(true, base, cur); r.Outcome != OutcomeBreaking {
		t.Fatalf("Outcome = %v, want OutcomeBreaking\noutput:\n%s\nerr: %v", r.Outcome, r.Output, r.Err)
	}
	if r := CheckProto(true, base, base); r.Outcome != OutcomePass {
		t.Fatalf("identical protos: Outcome = %v, want OutcomePass", r.Outcome)
	}
}

// newRepo makes a git repo with one committed file and then modifies it on
// the worktree, returning the repo dir. The baseline under test is the
// committed content, which exists nowhere on disk — that is the whole point.
func newRepo(t *testing.T, rel, committed, current string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(path, []byte(committed), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "base")
	if err := os.WriteFile(path, []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// spec: R-CLI-7
//
// R-CLI-7 says -baseline is "a git ref or file path", and the flag's own help
// text promises the same. A git ref names content that exists only in git
// history, so handing it to oasdiff as a filename fails with "open HEAD: no
// such file or directory" — the promised half of the flag never worked.
func TestResolveOpenAPIBaseline_GitRef(t *testing.T) {
	const committed = `openapi: 3.0.0
info: {title: demo, version: 1.0.0}
paths:
  /health: {get: {responses: {'200': {description: ok}}}}
  /orders: {get: {responses: {'200': {description: ok}}}}
`
	const current = `openapi: 3.0.0
info: {title: demo, version: 1.0.0}
paths:
  /health: {get: {responses: {'200': {description: ok}}}}
`
	dir := newRepo(t, "openapi.yaml", committed, current)
	spec := filepath.Join(dir, "openapi.yaml")

	got, cleanup, err := ResolveOpenAPIBaseline("HEAD", spec)
	if err != nil {
		t.Fatalf("ResolveOpenAPIBaseline: %v", err)
	}
	defer cleanup()
	raw, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read resolved baseline: %v", err)
	}
	if string(raw) != committed {
		t.Fatalf("resolved baseline is not the committed content:\n%s", raw)
	}

	// End to end: the committed spec has /orders, the worktree does not, so
	// this must come back breaking rather than erroring on a missing file.
	realTool(t, OASDiff)
	if r := CheckOpenAPI(true, got, spec); r.Outcome != OutcomeBreaking {
		t.Fatalf("Outcome = %v, want OutcomeBreaking\noutput:\n%s\nerr: %v", r.Outcome, r.Output, r.Err)
	}
}

// spec: R-CLI-7
//
// An existing file path must be used as-is — resolving it through git would
// break the other half of the flag's contract.
func TestResolveOpenAPIBaseline_FilePathPassesThrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "base.yaml")
	if err := os.WriteFile(path, []byte("openapi: 3.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, cleanup, err := ResolveOpenAPIBaseline(path, filepath.Join(dir, "openapi.yaml"))
	if err != nil {
		t.Fatalf("ResolveOpenAPIBaseline: %v", err)
	}
	defer cleanup()
	if got != path {
		t.Errorf("got %q, want the path unchanged (%q)", got, path)
	}
}

// spec: R-CLI-7
//
// A ref that does not exist must be an error naming the ref, not a silent
// fallback to comparing the spec against itself — which would report "no
// breaking changes" for every possible change.
func TestResolveOpenAPIBaseline_UnknownRefErrors(t *testing.T) {
	dir := newRepo(t, "openapi.yaml", "openapi: 3.0.0\n", "openapi: 3.0.1\n")
	_, _, err := ResolveOpenAPIBaseline("no-such-ref", filepath.Join(dir, "openapi.yaml"))
	if err == nil {
		t.Fatal("want an error for an unresolvable ref")
	}
	if !strings.Contains(err.Error(), "no-such-ref") {
		t.Errorf("error should name the ref, got: %v", err)
	}
}

// spec: R-CLI-7
//
// buf reads a git baseline through its own input syntax (".git#ref=...")
// rather than a materialised file; a bare ref makes buf exit 1 with "had no
// .proto files", which the old exit mapping would have relayed as a breaking
// change — a false positive from a baseline that never loaded.
func TestResolveProtoBaseline_GitRef(t *testing.T) {
	const committed = "syntax = \"proto3\";\npackage demo;\nmessage Order { string id = 1; int32 qty = 2; }\n"
	const current = "syntax = \"proto3\";\npackage demo;\nmessage Order { string id = 1; }\n"
	dir := newRepo(t, "proto/a.proto", committed, current)
	if err := os.WriteFile(filepath.Join(dir, "proto", "buf.yaml"), []byte("version: v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	protoDir := filepath.Join(dir, "proto")

	got, err := ResolveProtoBaseline("HEAD", protoDir)
	if err != nil {
		t.Fatalf("ResolveProtoBaseline: %v", err)
	}
	if !strings.Contains(got, "ref=HEAD") {
		t.Fatalf("got %q, want buf's git input syntax naming the ref", got)
	}

	realTool(t, Buf)
	if r := CheckProto(true, got, protoDir); r.Outcome != OutcomeBreaking {
		t.Fatalf("Outcome = %v, want OutcomeBreaking\noutput:\n%s\nerr: %v", r.Outcome, r.Output, r.Err)
	}
}
