package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jd316/tortureu/internal/fault"
)

// dockerAvailable skips a test rather than passing it when no Docker daemon
// is reachable — a Docker-dependent guarantee proven only "when Docker
// happens to be there" would be worse than no test (per the Task 7 brief).
func dockerAvailable(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}
}

func startTestContainer(t *testing.T) string {
	t.Helper()
	// No --rm: the kill test needs the container to still exist (just
	// stopped) after being killed, so `docker start` can restart it.
	out, err := exec.Command("docker", "run", "-d", "alpine:3.20", "sleep", "120").Output()
	if err != nil {
		t.Fatalf("docker run: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	return id
}

func containerState(t *testing.T, id, format string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", "-f", format, id).Output()
	if err != nil {
		t.Fatalf("docker inspect: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// spec: R-EXE-6
func TestDockerApplier_PauseAndUndoUnpauseAgainstRealContainer(t *testing.T) {
	dockerAvailable(t)
	id := startTestContainer(t)

	a := DockerApplier{}
	undo, err := a.ApplyDocker("f1", fault.DockerAction{Kind: "pause", Container: id})
	if err != nil {
		t.Fatalf("ApplyDocker(pause): %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := containerState(t, id, "{{.State.Paused}}"); got != "true" {
		t.Fatalf("State.Paused = %q, want true", got)
	}

	if err := undo(); err != nil {
		t.Fatalf("undo: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := containerState(t, id, "{{.State.Paused}}"); got != "false" {
		t.Fatalf("State.Paused after undo = %q, want false — R-EXE-5 requires teardown to actually reverse the fault", got)
	}
}

// spec: R-EXE-6
func TestDockerApplier_KillAndUndoRestartsContainer(t *testing.T) {
	dockerAvailable(t)
	id := startTestContainer(t)

	a := DockerApplier{}
	undo, err := a.ApplyDocker("f1", fault.DockerAction{Kind: "kill", Container: id})
	if err != nil {
		t.Fatalf("ApplyDocker(kill): %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := containerState(t, id, "{{.State.Running}}"); got != "false" {
		t.Fatalf("State.Running after kill = %q, want false", got)
	}

	if err := undo(); err != nil {
		t.Fatalf("undo: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := containerState(t, id, "{{.State.Running}}"); got != "true" {
		t.Fatalf("State.Running after undo = %q, want true", got)
	}
}

// spec: R-EXE-6
func TestDockerApplier_StressCPUPassesCPULoadPercentToStressNG(t *testing.T) {
	// B1 round 1 measured `cpu: 90%, workers: 4` (torture.example.yaml's own
	// documented syntax) producing ~403-416% of one core: cpu_percent was
	// computed by internal/fault and never read here, so stress-ng ran at
	// its own default (full load per worker) regardless of what was
	// requested. B1 round 2, after --cpu-load was wired in unconditionally,
	// measured ~360.7% instead of the requested 90%: --cpu-load 90 was
	// applied to all 4 workers verbatim (4 x 90% = 360%), so cpu_percent
	// meant "load per worker" instead of "load for the container as a
	// whole" — SPEC's `cpu: N%` is a total-load request, and workers is a
	// separate parallelism modifier (how many stress-ng processes divide
	// that load), not a multiplier on it. The fix divides cpu_percent by
	// workers so N workers at N%/workers each sum back to the requested
	// total. No live Docker daemon needed: a fake "docker" that only
	// records its own argv is enough, since the property under test is
	// which flags this package builds, not stress-ng's actual behavior.
	dir := t.TempDir()
	capturedPath := filepath.Join(dir, "captured-args.txt")
	fakeDocker := "#!/bin/sh\necho \"$@\" >> " + capturedPath + "\n"
	fakeDockerPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(fakeDockerPath, []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}

	a := DockerApplier{Bin: fakeDockerPath}
	if _, err := a.ApplyDocker("cpu_squeeze", fault.DockerAction{
		Kind:      "stress",
		Container: "checkout-api",
		Args:      map[string]any{"resource": "cpu", "workers": 4, "cpu_percent": 90},
	}); err != nil {
		t.Fatalf("ApplyDocker: %v", err)
	}

	captured, err := os.ReadFile(capturedPath)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	// 90% total / 4 workers = 22.5, rounded to 23 (4 x 23 = 92, within
	// stress-ng's own integer-worker approximation of the requested total —
	// the same class of honest approximation already documented for
	// duration-based faults, R-EXE-22).
	if !strings.Contains(string(captured), "--cpu-load 23") {
		t.Errorf("captured docker exec args = %q, want to contain \"--cpu-load 23\" (90%% requested / 4 workers) — cpu_percent must be divided across workers so the requested total load is what's actually applied, not requested%%-per-worker", captured)
	}
	if strings.Contains(string(captured), "--cpu-load 90") {
		t.Errorf("captured docker exec args = %q still contains the undivided 90%% per worker — with workers=4 this applies 360%% total, not the requested 90%%", captured)
	}
}

// spec: R-EXE-6
func TestDockerApplier_StressCPUSingleWorkerAppliesRequestedPercentUnchanged(t *testing.T) {
	// Regression guard for the workers-division fix above: with no workers
	// modifier (the common case — 1 implicit worker), cpu_percent must
	// still pass through unchanged, not accidentally divided by some
	// default > 1.
	dir := t.TempDir()
	capturedPath := filepath.Join(dir, "captured-args.txt")
	fakeDocker := "#!/bin/sh\necho \"$@\" >> " + capturedPath + "\n"
	fakeDockerPath := filepath.Join(dir, "docker")
	if err := os.WriteFile(fakeDockerPath, []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}

	a := DockerApplier{Bin: fakeDockerPath}
	if _, err := a.ApplyDocker("cpu_squeeze", fault.DockerAction{
		Kind:      "stress",
		Container: "checkout-api",
		Args:      map[string]any{"resource": "cpu", "cpu_percent": 90},
	}); err != nil {
		t.Fatalf("ApplyDocker: %v", err)
	}

	captured, err := os.ReadFile(capturedPath)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	if !strings.Contains(string(captured), "--cpu-load 90") {
		t.Errorf("captured docker exec args = %q, want to contain \"--cpu-load 90\" (no workers modifier = 1 implicit worker, requested percent unchanged)", captured)
	}
}

// spec: R-CFG-15
func TestDockerApplier_KillAndGracefulSendGenuinelyDistinctSignals(t *testing.T) {
	// R-CFG-15: pause, kill, and graceful MUST remain distinct verbs,
	// producing three different client-observable behaviors. A B1 finding
	// showed kill producing a graceful close instead of an abrupt reset —
	// internal/fault's owner confirmed this is an applier defect (it
	// already emits distinct Kind values for all three) and closed their
	// side by adding DockerAction.Args["signal"]. This proves this
	// package's half: kill and graceful actually deliver different
	// signals, not the same Docker-level mechanism twice.
	//
	// The proof: a container trapping SIGTERM to exit 0 cleanly makes the
	// two cases unambiguous by Docker's own exit-code convention alone —
	// SIGTERM (graceful) is caught by the trap and exits 0; SIGKILL (kill)
	// cannot be trapped by any process (the one signal the kernel never
	// lets userspace intercept), so Docker reports the standard
	// killed-by-signal exit code 137 (128+9) instead. If kill and graceful
	// ever collapse back into sending the same signal, this test cannot
	// tell them apart and fails.
	dockerAvailable(t)

	startTrapContainer := func(t *testing.T) string {
		t.Helper()
		out, err := exec.Command("docker", "run", "-d", "alpine:3.20", "sh", "-c",
			"trap 'exit 0' TERM; while true; do sleep 1; done").Output()
		if err != nil {
			t.Fatalf("docker run: %v", err)
		}
		id := strings.TrimSpace(string(out))
		t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
		return id
	}

	waitExited := func(t *testing.T, id string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if containerState(t, id, "{{.State.Running}}") == "false" {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("container %s never exited", id)
	}

	t.Run("graceful sends SIGTERM: trap catches it, exits cleanly", func(t *testing.T) {
		id := startTrapContainer(t)
		a := DockerApplier{}
		if _, err := a.ApplyDocker("f1", fault.DockerAction{
			Kind: "graceful", Container: id, Args: map[string]any{"signal": "SIGTERM"},
		}); err != nil {
			t.Fatalf("ApplyDocker(graceful): %v", err)
		}
		waitExited(t, id)
		if got := containerState(t, id, "{{.State.ExitCode}}"); got != "0" {
			t.Errorf("ExitCode = %s, want 0 — the trap should have caught SIGTERM and exited cleanly; a non-zero code means graceful did not actually send SIGTERM", got)
		}
	})

	t.Run("kill sends SIGKILL: the trap cannot catch it", func(t *testing.T) {
		id := startTrapContainer(t)
		a := DockerApplier{}
		if _, err := a.ApplyDocker("f1", fault.DockerAction{
			Kind: "kill", Container: id, Args: map[string]any{"signal": "SIGKILL"},
		}); err != nil {
			t.Fatalf("ApplyDocker(kill): %v", err)
		}
		waitExited(t, id)
		if got := containerState(t, id, "{{.State.ExitCode}}"); got != "137" {
			t.Errorf("ExitCode = %s, want 137 (128+SIGKILL) — if this is 0, kill delivered SIGTERM (or something the trap could catch), collapsing it into the same class as graceful", got)
		}
	})
}
