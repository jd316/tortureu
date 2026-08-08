package main

import (
	"strings"
	"testing"
)

// spec: R-CLI-5
//
// k6 is not a prerequisite. R-DC2-3's internal: true topology means Docker
// publishes no host port for the SUT, so `run` always executes k6 in the
// pinned grafana/k6 image sharing the SUT's network namespace — the whole
// test suite and the whole eval corpus run on a machine with no k6.
//
// doctor used to report "[missing] k6 — install: https://k6.io/..." as one
// of the three things a run needs, which sent a new user to install a tool
// the tool never uses, on their very first command.
func TestCheckPrerequisites_K6IsNotRequired(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // genuine absence, not a stub

	var k6 *PrereqCheck
	for i, c := range checkPrerequisites() {
		if c.Name == "k6" {
			k6 = &checkPrerequisites()[i]
		}
	}
	if k6 == nil {
		return // dropping it entirely is also a valid answer
	}
	if k6.Required {
		t.Error("k6 is reported as required; run uses the pinned container and never the host binary")
	}
	if !strings.Contains(strings.ToLower(k6.Hint), "container") {
		t.Errorf("hint = %q, want it to say the container is used instead", k6.Hint)
	}
	if strings.Contains(k6.Hint, "install:") {
		t.Errorf("hint = %q, still tells the user to install a tool run does not use", k6.Hint)
	}
}

// spec: R-CLI-5
//
// docker and docker compose ARE required, and must stay so.
func TestCheckPrerequisites_DockerRemainsRequired(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, c := range checkPrerequisites() {
		if c.Name == "docker" || c.Name == "docker compose" {
			if !c.Required {
				t.Errorf("%s must be reported as required", c.Name)
			}
			if c.Hint == "" {
				t.Errorf("%s is missing and carries no install hint", c.Name)
			}
		}
	}
}

// spec: R-CLI-5
//
// `init` warns under the header "this machine is missing what `run` needs".
// An optional tool listed there contradicts its own hint — the entry read
// "k6: not required — run executes it in a container" under a header saying
// it was needed. Only required tools belong in that list.
func TestMissingPrerequisites_ExcludesOptionalTools(t *testing.T) {
	checks := []PrereqCheck{
		{Name: "k6", Found: false, Hint: "not required — container is used"},
		{Name: "docker", Found: false, Required: true, Hint: "install: ..."},
		{Name: "docker compose", Found: true, Required: true},
	}
	got := missingPrerequisites(checks)
	if len(got) != 1 || got[0].Name != "docker" {
		var names []string
		for _, c := range got {
			names = append(names, c.Name)
		}
		t.Fatalf("missingPrerequisites = %v, want only [docker]", names)
	}
}
