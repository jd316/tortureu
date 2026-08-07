package run

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/fault"
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
