package emit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
)

// spec: R-CLI-8

const porcupineFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://localhost:8080
egress:
  default: deny
  hosts:
    etcd:2379: { class: internal }
load:
  engine: k6
  model: arrival_rate
  stages:
    - phase: peak
      hold: 500rps
      for: 60s
faults:
  - name: etcd_partition
    at: peak
    for: 30s
    target: etcd:2379
    inject: { down: true }
assert:
  - http_req_duration: ["p(95)<500"]
`

func porcupineSystem() *detect.System {
	return &detect.System{Deps: []detect.Dep{{Name: "etcd", Type: "etcd", Address: "etcd:2379"}}}
}

// spec: R-CLI-8 — registry.yaml gates porcupine on
// dep:etcd|dep:consul|dep:zookeeper. With none detected there is no
// linearizable store to check, and the honest answer is to say so.
func TestPorcupine_RefusesWithoutLinearizableDependency(t *testing.T) {
	cfg := mustParse(t, porcupineFixture)
	out, err := Porcupine(cfg, &detect.System{Deps: []detect.Dep{{Name: "redis", Type: "redis"}}})
	if err != nil {
		t.Fatalf("Porcupine: %v", err)
	}
	if !strings.Contains(out, "dep:etcd") || !strings.Contains(out, "nothing to emit") {
		t.Errorf("expected a refusal naming the gating predicate:\n%s", out)
	}
	if strings.Contains(out, "package main") {
		t.Errorf("refusal must not still emit a checker program:\n%s", out)
	}
	nilOut, err := Porcupine(cfg, nil)
	if err != nil {
		t.Fatalf("Porcupine(nil): %v", err)
	}
	if !strings.Contains(nilOut, "could not be detected") {
		t.Errorf("a nil system must say detection did not run:\n%s", nilOut)
	}
}

// spec: R-CLI-8 — the emitted artefact is a runnable Go program built on
// the real porcupine library, not a description of one.
func TestPorcupine_EmitsCheckerProgram(t *testing.T) {
	out, err := Porcupine(mustParse(t, porcupineFixture), porcupineSystem())
	if err != nil {
		t.Fatalf("Porcupine: %v", err)
	}
	for _, want := range []string{
		"package main",
		"github.com/anishathalye/porcupine",
		"porcupine.CheckOperationsVerbose",
		"porcupine.Model",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("emitted program is missing %q:\n%s", want, out)
		}
	}
}

// spec: R-CLI-8 — an undecided check (porcupine's Unknown, i.e. the
// search timed out) must never be reported as linearizable, and an empty
// history must never be reported as a pass: both are "we could not
// check", not "it was fine".
func TestPorcupine_UnknownAndEmptyHistoryAreNotPasses(t *testing.T) {
	out, err := Porcupine(mustParse(t, porcupineFixture), porcupineSystem())
	if err != nil {
		t.Fatalf("Porcupine: %v", err)
	}
	if !strings.Contains(out, "porcupine.Unknown") {
		t.Errorf("the emitted program must handle porcupine.Unknown explicitly:\n%s", out)
	}
	if !strings.Contains(out, "empty history") {
		t.Errorf("the emitted program must refuse an empty history:\n%s", out)
	}
}

// spec: R-CLI-8 — R-CLI-13 added call_ns/return_ns to the cassette, so the
// timestamp blocker this emit used to document is gone. What survives is a
// different limit: a cassette records HTTP exchanges, and deciding which
// exchange is a get or a put on which key needs the application's own
// semantics. Guessing that would fabricate the operations the checker
// reasons over, exactly as inventing timestamps once would have. The
// emitted output must state the surviving limit and must NOT still claim
// cassettes lack call/return times.
func TestPorcupine_DisclosesWhatACassetteStillCannotSupply(t *testing.T) {
	out, err := Porcupine(mustParse(t, porcupineFixture), porcupineSystem())
	if err != nil {
		t.Fatalf("Porcupine: %v", err)
	}
	if !strings.Contains(out, "cassette") {
		t.Errorf("the cassette limitation must be stated in the emitted output:\n%s", out)
	}
	for _, want := range []string{"call_ns", "return_ns"} {
		if !strings.Contains(out, want) {
			t.Errorf("the required history schema field %q is not documented:\n%s", want, out)
		}
	}
	// The superseded claim must be gone, not merely supplemented — a stale
	// "cassettes have no timestamps" tells the reader something false now.
	for _, stale := range []string{"NO\n// absolute call/return times", "never an absolute call or return time"} {
		if strings.Contains(out, stale) {
			t.Errorf("output still carries the superseded no-timestamps claim %q:\n%s", stale, out)
		}
	}
}

// spec: R-CLI-8 — porcupine consumes a history; it injects nothing, so
// every fault must be reported as untranslated rather than dropped.
func TestPorcupine_ReportsFaultsNotTranslated(t *testing.T) {
	out, err := Porcupine(mustParse(t, porcupineFixture), porcupineSystem())
	if err != nil {
		t.Fatalf("Porcupine: %v", err)
	}
	if !strings.Contains(out, "etcd_partition") || !strings.Contains(out, "not translated by porcupine") {
		t.Errorf("the fault must be reported as not translated:\n%s", out)
	}
}

// spec: R-CLI-8 — the header records what was verified against a real
// porcupine build, and what was not.
func TestPorcupine_HeaderRecordsWhatWasVerified(t *testing.T) {
	if !strings.Contains(porcupineHeader, "VERIFICATION STATUS") {
		t.Fatal("porcupineHeader must carry a VERIFICATION STATUS block")
	}
	if !strings.Contains(porcupineHeader, "anishathalye/porcupine") {
		t.Error("porcupineHeader does not name the library it was verified against")
	}
}

// spec: R-CLI-8 — `tortureu emit porcupine` must dispatch, and needs
// detection to know whether a linearizable store is present.
func TestPorcupine_RegisteredWithEmit(t *testing.T) {
	found := false
	for _, name := range Tools() {
		if name == "porcupine" {
			found = true
		}
	}
	if !found {
		t.Fatalf("porcupine is not registered; Tools() = %v", Tools())
	}
	if !NeedsSystem("porcupine") {
		t.Error("porcupine must declare needsSystem: its gate is a detected dependency")
	}
	if _, err := Emit("porcupine", mustParse(t, porcupineFixture), porcupineSystem()); err != nil {
		t.Errorf("Emit(\"porcupine\"): %v", err)
	}
}

// spec: R-CLI-8 — the emitted program is compiled and run for real against
// the porcupine library: a known-linearizable history must exit 0 and a
// known-non-linearizable one must exit 1. Opt-in (network + Go module
// download required), mirroring protocol_test.go's TORTUREU_DOCKER_TESTS
// gate; porcupine.go's header records the recorded result of this run.
func TestPorcupine_EmittedProgramCompilesAndChecksRealHistories(t *testing.T) {
	if os.Getenv("TORTUREU_NET_TESTS") != "1" {
		t.Skip("set TORTUREU_NET_TESTS=1 to compile and run the emitted porcupine checker (downloads the porcupine module)")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	src, err := Porcupine(mustParse(t, porcupineFixture), porcupineSystem())
	if err != nil {
		t.Fatalf("Porcupine: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"mod", "init", "torturecheck"},
		{"get", "github.com/anishathalye/porcupine"},
		{"build", "-o", "check", "."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
	}

	// A sequential, consistent history: put(x,1) then get(x)=1.
	good := `{"client":0,"op":"put","key":"x","value":"1","call_ns":0,"return_ns":10}
{"client":0,"op":"get","key":"x","value":"1","call_ns":20,"return_ns":30}
`
	// The same history with a read that no linearization can explain.
	bad := `{"client":0,"op":"put","key":"x","value":"1","call_ns":0,"return_ns":10}
{"client":0,"op":"get","key":"x","value":"2","call_ns":20,"return_ns":30}
`
	for _, tc := range []struct {
		name    string
		history string
		want    int
	}{
		{"linearizable", good, 0},
		{"not linearizable", bad, 1},
		{"empty", "", 2},
	} {
		path := filepath.Join(dir, tc.name+".jsonl")
		if err := os.WriteFile(path, []byte(tc.history), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(filepath.Join(dir, "check"), path).CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if code != tc.want {
			t.Errorf("%s history: exit %d, want %d\n%s", tc.name, code, tc.want, out)
		}
	}
}
