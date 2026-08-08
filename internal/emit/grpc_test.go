package emit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/detect"
)

// spec: R-CLI-8

const grpcFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://localhost:8080
egress:
  default: deny
  hosts:
    postgres:5432: { class: internal }
load:
  engine: k6
  model: arrival_rate
  stages:
    - phase: ramp_up
      to: 500rps
      over: 60s
    - phase: peak
      hold: 500rps
      for: 180s
  scenarios:
    - name: checkout
      weight: 70
      flow:
        - { method: POST, path: /checkout }
faults:
  - name: pg_slow
    at: peak
    for: 60s
    target: postgres:5432
    inject: { latency: 300ms }
  - name: cpu_squeeze
    at: peak
    for: 20s
    target: checkout-api
    inject: { cpu: 90%, workers: 4 }
assert:
  - http_req_duration: ["p(95)<500"]
`

func grpcProtoSystem() *detect.System {
	return &detect.System{Coverage: detect.Coverage{Proto: true}}
}

// spec: R-CLI-8 — a hold: stage is ghz's constant-rate schedule: one
// --rps at the declared rate for the declared duration.
func TestGhz_HoldStageEmitsConstantRPS(t *testing.T) {
	cfg := mustParse(t, grpcFixture)
	out, err := Ghz(cfg, grpcProtoSystem())
	if err != nil {
		t.Fatalf("Ghz: %v", err)
	}
	if !strings.Contains(out, "--rps=500") {
		t.Errorf("hold: 500rps did not become --rps=500:\n%s", out)
	}
	if !strings.Contains(out, "--duration=180s") {
		t.Errorf("for: 180s did not become --duration=180s:\n%s", out)
	}
}

// spec: R-CLI-8 — a to:/over: ramp is ghz's "line" load schedule, whose
// slope is the declared rate change per second. Emitting it as a constant
// rate would silently drop the ramp the config asked for.
func TestGhz_RampStageEmitsLineSchedule(t *testing.T) {
	cfg := mustParse(t, grpcFixture)
	out, err := Ghz(cfg, grpcProtoSystem())
	if err != nil {
		t.Fatalf("Ghz: %v", err)
	}
	for _, want := range []string{
		"--load-schedule=line",
		// Not --load-start=0: the real ghz binary PANICS on a zero start
		// rate ("LinearPacer.Start cannot be 0"), so a ramp from rest
		// starts at 1 rps and the substitution is disclosed below.
		"--load-start=1",
		"--load-end=500",
		"--load-step-duration=1s",
		"--duration=60s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ramp_up (0 -> 500rps over 60s) is missing %q:\n%s", want, out)
		}
	}
	// 500rps over 60s is 8.33 rps/s; ghz's --load-step is an integer, so
	// the emitted step must be the rounded value AND say it was rounded.
	if !strings.Contains(out, "--load-step=8") {
		t.Errorf("expected --load-step=8 (500/60 rounded):\n%s", out)
	}
	if !strings.Contains(out, "rounded") {
		t.Errorf("the integer rounding of the ramp slope must be disclosed:\n%s", out)
	}
	if !strings.Contains(out, "cannot be 0") {
		t.Errorf("substituting 1 rps for a declared start of 0 must be disclosed:\n%s", out)
	}
}

// spec: R-CLI-8 — every fault must be reported as untranslated, never
// dropped: ghz is a load generator and injects nothing.
func TestGhz_ReportsEveryFaultItDoesNotTranslate(t *testing.T) {
	cfg := mustParse(t, grpcFixture)
	out, err := Ghz(cfg, grpcProtoSystem())
	if err != nil {
		t.Fatalf("Ghz: %v", err)
	}
	for _, name := range []string{"pg_slow", "cpu_squeeze"} {
		if !strings.Contains(out, name) {
			t.Errorf("fault %q is missing from the output entirely:\n%s", name, out)
		}
	}
	if strings.Count(out, "not translated by ghz") < 2 {
		t.Errorf("both faults must be reported as not translated:\n%s", out)
	}
}

// spec: R-CLI-8 — an HTTP scenario flow is not a gRPC method call, so
// every scenario must be reported as untranslated rather than silently
// ignored (the same silent-omission failure the fault rule forbids).
func TestGhz_ReportsScenariosNotTranslated(t *testing.T) {
	cfg := mustParse(t, grpcFixture)
	out, err := Ghz(cfg, grpcProtoSystem())
	if err != nil {
		t.Fatalf("Ghz: %v", err)
	}
	if !strings.Contains(out, "checkout") || !strings.Contains(out, "not translated by ghz") {
		t.Errorf("scenario \"checkout\" must be reported as not translated:\n%s", out)
	}
}

// spec: R-CLI-8 — torture.yaml carries no gRPC address, no .proto path and
// no method name. None may be invented: target.base_url is an HTTP
// address, and assuming the gRPC server shares its port would be a guess.
func TestGhz_NeverGuessesAddressProtoOrCall(t *testing.T) {
	cfg := mustParse(t, grpcFixture)
	out, err := Ghz(cfg, grpcProtoSystem())
	if err != nil {
		t.Fatalf("Ghz: %v", err)
	}
	for _, want := range []string{"TORTUREU_GRPC_ADDR", "TORTUREU_PROTO", "TORTUREU_CALL"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected the unknowable %s to be a required placeholder:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ghz ") && strings.Contains(line, "localhost:8080") {
			t.Errorf("target.base_url's HTTP authority was used as the gRPC address: %q", line)
		}
	}
}

// spec: R-CLI-8 — registry.yaml gates ghz on spec:proto. With detection
// reporting no .proto files, the honest answer is to refuse and say so,
// not to emit a command against a service that speaks no gRPC.
func TestGhz_RefusesWhenNoProtoDetected(t *testing.T) {
	cfg := mustParse(t, grpcFixture)
	out, err := Ghz(cfg, &detect.System{Coverage: detect.Coverage{Proto: false}})
	if err != nil {
		t.Fatalf("Ghz: %v", err)
	}
	if !strings.Contains(out, "spec:proto") || !strings.Contains(out, "nothing to emit") {
		t.Errorf("expected a refusal naming spec:proto:\n%s", out)
	}
	if strings.Contains(out, "--rps=") {
		t.Errorf("refusal must not still emit a ghz command:\n%s", out)
	}
}

// spec: R-CLI-8 — a nil *detect.System means detection never ran, which is
// a different statement from "no .proto files exist"; conflating the two
// would blame the user's repo for our own missing input.
func TestGhz_NilSystemSaysDetectionDidNotRun(t *testing.T) {
	cfg := mustParse(t, grpcFixture)
	out, err := Ghz(cfg, nil)
	if err != nil {
		t.Fatalf("Ghz: %v", err)
	}
	if !strings.Contains(out, "could not be detected") {
		t.Errorf("expected the nil-system case to say detection did not run:\n%s", out)
	}
}

// spec: R-CLI-8 — the header records exactly what was and was not verified
// against a real ghz binary. Pinned so the claim cannot quietly grow.
func TestGhz_HeaderRecordsWhatWasVerified(t *testing.T) {
	if !strings.Contains(ghzHeader, "VERIFICATION STATUS") {
		t.Fatal("ghzHeader must carry a VERIFICATION STATUS block")
	}
	for _, want := range []string{"ghcr.io/bojand/ghz", "moul/grpcbin", "load-schedule=line"} {
		if !strings.Contains(ghzHeader, want) {
			t.Errorf("ghzHeader does not record %q as part of what was verified", want)
		}
	}
}

// spec: R-CLI-8 — the emitted script is executed for real: a short load
// profile is emitted, given a real gRPC address/proto/call, and run
// against a live moul/grpcbin container through the real ghz binary
// (ghcr.io/bojand/ghz). Opt-in via TORTUREU_DOCKER_TESTS=1, mirroring
// protocol_test.go's live sysbench check; grpc.go's ghzHeader records the
// result of the run this test performs.
func TestGhz_EmittedScriptRunsAgainstRealGRPCServer(t *testing.T) {
	if os.Getenv("TORTUREU_DOCKER_TESTS") != "1" {
		t.Skip("set TORTUREU_DOCKER_TESTS=1 to run the emitted ghz script against a live gRPC server")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	const shortFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://localhost:8080
load:
  engine: k6
  model: arrival_rate
  stages:
    - phase: ramp_up
      to: 40rps
      over: 4s
    - phase: peak
      hold: 40rps
      for: 3s
assert:
  - http_req_duration: ["p(95)<500"]
`
	out, err := Ghz(mustParse(t, shortFixture), grpcProtoSystem())
	if err != nil {
		t.Fatalf("Ghz: %v", err)
	}

	const container = "tortureu-ghz-grpcbin"
	exec.Command("docker", "rm", "-f", container).Run()
	if err := exec.Command("docker", "run", "-d", "--name", container, "moul/grpcbin").Run(); err != nil {
		t.Skipf("could not start moul/grpcbin: %v", err)
	}
	defer exec.Command("docker", "rm", "-f", container).Run()
	time.Sleep(2 * time.Second)

	dir := t.TempDir()
	script := filepath.Join(dir, "ghz.sh")
	if err := os.WriteFile(script, []byte(out), 0o755); err != nil {
		t.Fatal(err)
	}
	// A shim named "ghz" that runs the real ghz image inside grpcbin's
	// network namespace, so the emitted flags are the ones executed.
	shim := "#!/bin/sh\nexec docker run --rm --network container:" + container +
		" ghcr.io/bojand/ghz:latest \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ghz"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", script)
	cmd.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		"TORTUREU_GRPC_ADDR=127.0.0.1:9000",
		"TORTUREU_CALL=grpcbin.GRPCBin/Index",
		"TORTUREU_PROTO=",
		"TORTUREU_GHZ_EXTRA_ARGS=--insecure",
	)
	res, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("emitted ghz script failed: %v\n%s", err, res)
	}
	if strings.Count(string(res), "Status code distribution") < 2 {
		t.Errorf("expected one real ghz run per stage:\n%s", res)
	}
	if !strings.Contains(string(res), "[OK]") {
		t.Errorf("no successful gRPC responses:\n%s", res)
	}
}

// spec: R-CLI-8 — `tortureu emit ghz` must dispatch, and must be told it
// needs detection (spec:proto is a detection fact, not a torture.yaml one).
func TestGhz_RegisteredWithEmit(t *testing.T) {
	found := false
	for _, name := range Tools() {
		if name == "ghz" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ghz is not registered; Tools() = %v", Tools())
	}
	if !NeedsSystem("ghz") {
		t.Error("ghz must declare needsSystem: it gates on detection's spec:proto fact")
	}
	if _, err := Emit("ghz", mustParse(t, grpcFixture), grpcProtoSystem()); err != nil {
		t.Errorf("Emit(\"ghz\"): %v", err)
	}
}
