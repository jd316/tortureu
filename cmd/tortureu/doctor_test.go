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
				{ID: "vegeta", Tier: "drive", When: "always", How: "tortureu smoke", Planned: "smoke"},
				{ID: "locust", Tier: "delegate", When: "lang:python", How: "tortureu emit locust", Planned: "emit"},
			}},
			{ID: "chaos-k8s", Name: "Kubernetes chaos", Tools: []doctor.Tool{
				{ID: "chaosmesh", Tier: "know", When: "platform:k8s", How: "kubectl apply -f chaosengine.yaml"},
			}},
			{ID: "contracts", Name: "Contract testing", Tools: []doctor.Tool{
				{ID: "oasdiff", Tier: "delegate", When: "spec:openapi", How: "oasdiff breaking old.yaml new.yaml"},
			}},
		},
	}
}

// fixturePrereqs stands in for a machine with everything present, so tests
// unrelated to the prerequisite preflight aren't coupled to this test
// process's real PATH.
func fixturePrereqs() []PrereqCheck {
	return []PrereqCheck{
		{Name: "k6", Found: true, Version: "k6 v0.49.0"},
		{Name: "docker", Found: true, Version: "Docker version 24.0.0"},
		{Name: "docker compose", Found: true, Version: "Docker Compose version v2.20.0"},
	}
}

// spec: R-CLI-3
func TestDoctorReportsUncoveredDomains(t *testing.T) {
	sys := &detect.System{Coverage: detect.Coverage{K8s: false}}
	report := buildDoctorReport(nil, fixtureRegistry(), sys, fixturePrereqs())
	if !strings.Contains(report, "uncovered domains: chaos-k8s") {
		t.Errorf("report did not name the uncovered domain:\n%s", report)
	}
}

// spec: R-CLI-3, R-SCOPE-4
func TestDoctorLabelsKnowTierSuggestionsWithTierAndTrigger(t *testing.T) {
	sys := &detect.System{Coverage: detect.Coverage{K8s: true}}
	report := buildDoctorReport(nil, fixtureRegistry(), sys, fixturePrereqs())
	if !strings.Contains(report, "[know] chaos-k8s/chaosmesh") {
		t.Errorf("suggestion missing tier label (R-SCOPE-4):\n%s", report)
	}
	if !strings.Contains(report, "trigger: platform:k8s") {
		t.Errorf("suggestion missing trigger condition (R-CLI-3):\n%s", report)
	}
}

// spec: R-SCOPE-3
//
// The gap this fixes: doctor named delegate and know entries but never
// drive — the tier that IS the product (k6, Toxiproxy, stress-ng, pgbench,
// ...). A user could learn what tortureu hands off and what it merely
// names, but never what it actually executes. R-SCOPE-3 requires one front
// door to all three declared depths; this asserts all three actually
// appear for a stack that triggers one of each.
func TestDoctorReportsAllThreeTiers(t *testing.T) {
	sys := &detect.System{Lang: "python", Coverage: detect.Coverage{K8s: true}}
	report := buildDoctorReport(nil, fixtureRegistry(), sys, fixturePrereqs())

	if !strings.Contains(report, "DRIVEN BY TORTUREU") {
		t.Errorf("report has no drive-tier section:\n%s", report)
	}
	if !strings.Contains(report, "[drive] load/k6") {
		t.Errorf("report does not name a drive-tier tool:\n%s", report)
	}
	if !strings.Contains(report, "[delegate") && !strings.Contains(report, "load/locust") {
		t.Errorf("report does not name a delegate-tier tool:\n%s", report)
	}
	if !strings.Contains(report, "[know] chaos-k8s/chaosmesh") {
		t.Errorf("report does not name a know-tier tool:\n%s", report)
	}
}

// spec: R-SCOPE-4
//
// A drive entry whose how: names a verb not implemented in v0 (here
// vegeta -> `tortureu smoke`, a stub) must still be labelled "· planned"
// and disclose the unimplemented verb — claiming tortureu drives something
// it cannot run today would be the same misrepresentation R-SCOPE-4
// forbids for delegate/know, just inverted.
func TestDoctorLabelsPlannedDriveEntryAsNotYetRunnable(t *testing.T) {
	report := buildDoctorReport(nil, fixtureRegistry(), &detect.System{}, fixturePrereqs())
	if !strings.Contains(report, "[drive · planned] load/vegeta") {
		t.Errorf("planned drive entry not labelled as planned:\n%s", report)
	}
	if !strings.Contains(report, `verb "smoke" not implemented in v0`) {
		t.Errorf("planned drive entry does not disclose the unimplemented verb:\n%s", report)
	}
	// And the entry that does work today must not carry the same
	// annotation — the two stay distinguishable.
	if strings.Contains(report, "[drive · planned] load/k6") {
		t.Errorf("k6 (a working verb) incorrectly labelled as planned:\n%s", report)
	}
}

// spec: R-SCOPE-3
//
// A domain with no tool matching the detected system must stay absent from
// both the DRIVEN and SUGGESTIONS sections — it is still named under
// "uncovered domains" (R-CLI-3), but must not be padded into a section
// with a placeholder or an inapplicable entry.
func TestDoctorDomainWithNoMatchingToolsStaysAbsentNotPadded(t *testing.T) {
	sys := &detect.System{} // no python, no k8s, no openapi: nothing in
	// "contracts" or "chaos-k8s" applies.
	report := buildDoctorReport(nil, fixtureRegistry(), sys, fixturePrereqs())

	if strings.Contains(report, "contracts/") {
		t.Errorf("an inapplicable domain's tool was padded into the report:\n%s", report)
	}
	if !strings.Contains(report, "uncovered domains:") || !strings.Contains(report, "contracts") {
		t.Errorf("the inapplicable domain must still be named as uncovered:\n%s", report)
	}
}

// spec: R-SCOPE-3
//
// The gap this fixes: doctor's suggestions previously only ever printed
// know-tier entries, so a `tortureu doctor` user never learned that a
// delegate-tier tool (config generated and handed off, e.g. Locust) applied
// to their stack — "all in one place" held for two of the three declared
// depths and silently dropped the third. A delegate entry that applies must
// now appear in the report.
func TestDoctorIncludesApplicableDelegateTierSuggestion(t *testing.T) {
	sys := &detect.System{Lang: "python", Coverage: detect.Coverage{K8s: false}}
	report := buildDoctorReport(nil, fixtureRegistry(), sys, fixturePrereqs())
	if !strings.Contains(report, "load/locust") {
		t.Errorf("delegate-tier suggestion missing from report:\n%s", report)
	}
	if !strings.Contains(report, "trigger: lang:python") {
		t.Errorf("delegate suggestion missing trigger condition:\n%s", report)
	}
}

// spec: R-SCOPE-4
//
// A planned entry (its how: names a verb — here "emit" — not implemented
// in v0) must be labelled distinctly and must not read as something a user
// can run today: this is the specific failure mode the fix must avoid
// while making delegate tools visible at all — "invisible" must not become
// "mis-instructing".
func TestDoctorLabelsPlannedDelegateSuggestionAsNotYetRunnable(t *testing.T) {
	sys := &detect.System{Lang: "python"}
	report := buildDoctorReport(nil, fixtureRegistry(), sys, fixturePrereqs())
	if !strings.Contains(report, "[delegate · planned] load/locust") {
		t.Errorf("planned delegate suggestion not labelled as planned:\n%s", report)
	}
	if !strings.Contains(report, `verb "emit" not implemented in v0`) {
		t.Errorf("planned suggestion does not disclose the unimplemented verb:\n%s", report)
	}
}

// spec: R-SCOPE-3, R-SCOPE-4
//
// A delegate entry with no planned: marker (its how: verb already works)
// must render as plain [delegate], not [delegate · planned] — the two must
// stay distinguishable.
func TestDoctorLabelsRunnableDelegateSuggestionWithoutPlannedMarker(t *testing.T) {
	sys := &detect.System{Coverage: detect.Coverage{OpenAPI: true}}
	report := buildDoctorReport(nil, fixtureRegistry(), sys, fixturePrereqs())
	if !strings.Contains(report, "[delegate] contracts/oasdiff") {
		t.Errorf("runnable delegate suggestion missing or mislabelled:\n%s", report)
	}
	if strings.Contains(report, "oasdiff") && strings.Contains(report, "contracts/oasdiff: oasdiff breaking old.yaml new.yaml (verb") {
		t.Errorf("runnable delegate suggestion incorrectly annotated as planned:\n%s", report)
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
	report := buildDoctorReport(findings, fixtureRegistry(), sys, fixturePrereqs())
	if !strings.Contains(report, "hint:") {
		t.Errorf("finding not labelled as a hint:\n%s", report)
	}
}

// spec: R-CLI-5
//
// R-CLI-5 was added after this task escalated that no requirement named a
// prerequisite preflight (previously cited here as "closest fit R-CLI-3");
// re-pointed at the requirement written to describe it.
//
// This stubs PATH to a directory containing no binaries at all, so k6 is
// genuinely absent from the real exec.LookPath the check performs — not a
// mocked-away LookPath, which would prove nothing about whether the check
// actually detects absence.
func TestCheckPrerequisitesReportsGenuinelyMissingK6(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	checks := checkPrerequisites()

	var k6 *PrereqCheck
	for i := range checks {
		if checks[i].Name == "k6" {
			k6 = &checks[i]
		}
	}
	if k6 == nil {
		t.Fatal("checkPrerequisites did not report a k6 entry at all")
	}
	if k6.Found {
		t.Error("k6 reported Found with PATH pointing at an empty directory")
	}
	if k6.Hint == "" {
		t.Error("a missing prerequisite must carry an install hint")
	}
}

// spec: R-CLI-5
//
// The rendering side: buildDoctorReport must surface a missing
// prerequisite by name, with its hint, not silently drop it.
func TestBuildDoctorReportRendersMissingPrerequisite(t *testing.T) {
	// docker is the required one. k6 is present-or-not-required (R-CLI-5):
	// run executes it in a container, so its absence is not a problem to
	// fix and must not render as one.
	prereqs := []PrereqCheck{
		{Name: "k6", Found: false, Hint: "not required — run executes k6 in the pinned grafana/k6 container"},
		{Name: "docker", Found: false, Required: true, Hint: "install: https://docs.docker.com/get-docker/"},
		{Name: "docker compose", Found: true, Version: "Docker Compose version v2.20.0"},
	}
	report := buildDoctorReport(nil, fixtureRegistry(), &detect.System{}, prereqs)
	if !strings.Contains(report, "[missing] docker") {
		t.Errorf("report does not flag the missing required prerequisite:\n%s", report)
	}
	if !strings.Contains(report, "https://docs.docker.com/get-docker/") {
		t.Errorf("report does not carry the install hint:\n%s", report)
	}
	if strings.Contains(report, "[missing] k6") {
		t.Errorf("an optional tool is rendered as missing, which reads as a failure to fix:\n%s", report)
	}
	if !strings.Contains(report, "[n/a] k6") {
		t.Errorf("report does not mark k6 as not-required:\n%s", report)
	}
	if !strings.Contains(report, "[ok] docker compose") {
		t.Errorf("report does not confirm a present prerequisite:\n%s", report)
	}
}
