package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/doctor"
)

func fixtureRegistry() *doctor.Registry {
	return &doctor.Registry{
		Version: 0,
		Domains: []doctor.Domain{
			{ID: "load", Name: "Load generation", Tools: []doctor.Tool{
				{ID: "k6", Tier: "drive", When: "always", How: "tortureu run <scenario>"},
			}},
			{ID: "chaos-k8s", Name: "Kubernetes chaos", Tools: []doctor.Tool{
				{ID: "chaosmesh", Tier: "know", When: "platform:k8s", How: "kubectl apply -f chaosengine.yaml"},
			}},
		},
	}
}

// spec: R-CLI-3
func TestDoctorReportsUncoveredDomains(t *testing.T) {
	sys := &detect.System{Coverage: detect.Coverage{K8s: false}}
	report := buildDoctorReport(nil, fixtureRegistry(), sys)
	if !strings.Contains(report, "uncovered domains: chaos-k8s") {
		t.Errorf("report did not name the uncovered domain:\n%s", report)
	}
}

// spec: R-CLI-3, R-SCOPE-4
func TestDoctorLabelsKnowTierSuggestionsWithTierAndTrigger(t *testing.T) {
	sys := &detect.System{Coverage: detect.Coverage{K8s: true}}
	report := buildDoctorReport(nil, fixtureRegistry(), sys)
	if !strings.Contains(report, "[know] chaos-k8s/chaosmesh") {
		t.Errorf("suggestion missing tier label (R-SCOPE-4):\n%s", report)
	}
	if !strings.Contains(report, "trigger: platform:k8s") {
		t.Errorf("suggestion missing trigger condition (R-CLI-3):\n%s", report)
	}
}

// spec: R-COV-8
//
// The exact case that shipped broken: `doctor` run from a directory with no
// registry.yaml (i.e. any directory but this repo's root) must still
// succeed, because the default registry source is the one embedded in the
// binary, not a file read relative to the working directory.
func TestDoctorWorksOutsideRepoWithNoRegistryFileOnDisk(t *testing.T) {
	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("services:\n  app:\n    build: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "registry.yaml")); err == nil {
		t.Fatal("test setup: registry.yaml unexpectedly present")
	}

	t.Chdir(dir)

	var out, errb bytes.Buffer
	code := runDoctor([]string{"-compose", composePath}, &out, &errb)
	if code != 0 {
		t.Fatalf("doctor failed outside the repo (exit %d): %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "REGISTRY COVERAGE") {
		t.Errorf("doctor output missing coverage section:\n%s", out.String())
	}
}

// spec: R-AUD-3
func TestDoctorResilienceFindingsAreLabelledHints(t *testing.T) {
	sys := &detect.System{}
	findings := []doctor.Finding{
		{DepName: "postgres", Check: doctor.CheckTimeout, Level: doctor.LevelHint, Hint: "not configured", Experiment: "fault: latency on postgres"},
	}
	report := buildDoctorReport(findings, fixtureRegistry(), sys)
	if !strings.Contains(report, "hint:") {
		t.Errorf("finding not labelled as a hint:\n%s", report)
	}
}
