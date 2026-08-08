package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCompose(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	return p
}

const composeWithContainerName = `
services:
  api:
    build: .
    container_name: myapp-api
    ports: ["8080:8080"]
  db:
    image: postgres:16
`

const composeWithoutContainerName = `
services:
  api:
    build: .
    ports: ["8080:8080"]
  db:
    image: postgres:16
`

// spec: R-CLI-12 (proposed)
//
// The generated handoff must name the real compose file, the real SUT
// container, and keploy's real record subcommand — nothing invented.
func TestKeployPlanUsesDetectedComposeAndContainer(t *testing.T) {
	path := writeCompose(t, composeWithContainerName)

	plan, err := PlanKeploy(path)
	if err != nil {
		t.Fatalf("PlanKeploy: %v", err)
	}
	if plan.SUT != "api" {
		t.Errorf("SUT = %q, want api", plan.SUT)
	}
	if plan.ContainerName != "myapp-api" {
		t.Errorf("ContainerName = %q, want myapp-api", plan.ContainerName)
	}
	if !strings.Contains(plan.RecordCommand, "keploy record") {
		t.Errorf("RecordCommand = %q, want a keploy record command", plan.RecordCommand)
	}
	if !strings.Contains(plan.RecordCommand, "--container-name myapp-api") {
		t.Errorf("RecordCommand = %q, want --container-name myapp-api", plan.RecordCommand)
	}
	if !strings.Contains(plan.RecordCommand, path) {
		t.Errorf("RecordCommand = %q, want the real compose path %q", plan.RecordCommand, path)
	}
	// The config must be keploy's own key names, not ours.
	for _, key := range []string{"command:", "containerName: \"myapp-api\"", "path:"} {
		if !strings.Contains(plan.ConfigYAML, key) {
			t.Errorf("ConfigYAML missing %q:\n%s", key, plan.ConfigYAML)
		}
	}
}

// spec: R-CLI-12 (proposed)
//
// No container_name in the compose file means keploy's --container-name is
// unknowable. Guessing it produces a keploy run that records nothing and
// reports success, so this must refuse and say where to state it.
func TestKeployPlanRefusesWithoutContainerName(t *testing.T) {
	path := writeCompose(t, composeWithoutContainerName)

	_, err := PlanKeploy(path)
	if err == nil {
		t.Fatal("PlanKeploy: want refusal when compose states no container_name")
	}
	msg := err.Error()
	if !strings.Contains(msg, "container_name") {
		t.Errorf("error = %q, want it to name container_name", msg)
	}
	if !strings.Contains(msg, "api") {
		t.Errorf("error = %q, want it to name the SUT service it needs it on", msg)
	}
}

// spec: R-CLI-12 (proposed)
//
// A compose file with no build: service has no SUT (R-DET-8), so there is
// nothing to hand keploy. Reported, never guessed.
func TestKeployPlanRefusesWithoutSUT(t *testing.T) {
	path := writeCompose(t, "services:\n  db:\n    image: postgres:16\n")

	if _, err := PlanKeploy(path); err == nil {
		t.Fatal("PlanKeploy: want refusal when no SUT service is detected")
	}
}

// spec: R-CLI-12 (proposed)
// spec: R-CLI-5
//
// keploy is delegate tier: its absence is reported with an install hint,
// not as a failure of ours.
func TestKeployInstallHint(t *testing.T) {
	if !strings.Contains(KeployInstallHint, "keploy.io") {
		t.Errorf("KeployInstallHint = %q, want the official install source", KeployInstallHint)
	}
}

// spec: R-CLI-12 (proposed)
//
// The handoff prints a record command and a test command, and its notes
// must cover the timing flag each one actually needs. They are different
// flags: `keploy record` has --build-delay (default 30s) and no --delay;
// `keploy test` has BOTH --build-delay and its own -d/--delay (default
// 5s), the fixed wait before the first replayed request. Verified against
// keploy 3.6.11 `record --help` / `test --help`, and observed: the
// generated test command failed 3 of 4 recorded cases at the default
// delay and passed 4 of 4 at --delay 25 on the same recording. A note
// naming only --build-delay therefore leaves the user reading real
// replay failures as their application's fault.
func TestKeployHandoffNotesTestDelay(t *testing.T) {
	path := writeCompose(t, composeWithContainerName)
	plan, err := PlanKeploy(path)
	if err != nil {
		t.Fatalf("PlanKeploy: %v", err)
	}
	out := KeployHandoff(plan)

	if !strings.Contains(out, "--build-delay") {
		t.Errorf("handoff does not mention --build-delay for the record command:\n%s", out)
	}
	if !strings.Contains(out, "--delay") || !strings.Contains(out, "keploy test") {
		t.Errorf("handoff does not mention `keploy test`'s own --delay:\n%s", out)
	}
	// Naming the flag is not enough: --build-delay contains the substring
	// "-delay", so the note must state the default the user is up against.
	if !strings.Contains(out, "5s") {
		t.Errorf("handoff does not state `keploy test`'s 5s default delay:\n%s", out)
	}
}
