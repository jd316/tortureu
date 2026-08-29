package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/ci"
	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/detect"
)

// spec: R-CLI-1
func TestBuildInitWritesTargetAndEgressBlocks(t *testing.T) {
	sys := &detect.System{
		SUT:         "checkout-api",
		EgressClass: map[string]string{"postgres:5432": "internal", "api.stripe.com": "unclassified"},
	}
	out := buildInit(sys, "./docker-compose.yml")
	content := string(out.YAML)
	if !strings.Contains(content, "service: checkout-api") {
		t.Errorf("missing target.service:\n%s", content)
	}
	if !strings.Contains(content, "postgres:5432: { class: internal }") {
		t.Errorf("missing classified internal host:\n%s", content)
	}
	if strings.Contains(content, "api.stripe.com: { class:") {
		t.Errorf("unclassified host must not be assigned a class:\n%s", content)
	}
}

// spec: R-DC1-3 (via R-CLI-1's init->egress-manifest row / DC-2): an
// unclassified host must be left out of hosts: entirely, not guessed, so
// CheckUnclassified aborts the first run rather than silently allowing
// egress through a fabricated classification.
func TestBuildInitLeavesUnclassifiedHostsOutOfHostsBlockAndReportsGap(t *testing.T) {
	sys := &detect.System{
		EgressClass: map[string]string{"api.partner.com": "unclassified"},
	}
	out := buildInit(sys, "./docker-compose.yml")
	found := false
	for _, g := range out.Gaps {
		if strings.Contains(g, "api.partner.com") {
			found = true
		}
	}
	if !found {
		t.Errorf("gaps = %v, want the unclassified host surfaced", out.Gaps)
	}
}

// spec: R-DET-7 (init must surface detect.System.Gaps, never hide them)
func TestBuildInitSurfacesDetectionGaps(t *testing.T) {
	sys := &detect.System{Gaps: []string{"unrecognized image: weird/thing"}}
	out := buildInit(sys, "./docker-compose.yml")
	found := false
	for _, g := range out.Gaps {
		if g == "unrecognized image: weird/thing" {
			found = true
		}
	}
	if !found {
		t.Errorf("gaps = %v, want detect gap surfaced", out.Gaps)
	}
}

// spec: R-CLI-4
//
// This is the round-trip that would have caught the gap the coordinator
// found by hand: init succeeding while the file it wrote makes run refuse
// to start. buildInit's output must parse clean through internal/config,
// the same validation `tortureu run` applies, not just "look right".
func TestBuildInitOutputIsAcceptedByConfigParse(t *testing.T) {
	sys := &detect.System{
		SUT:         "checkout-api",
		EgressClass: map[string]string{"postgres:5432": "internal"},
	}
	out := buildInit(sys, "./docker-compose.yml")

	cfg, err := config.Parse(out.YAML)
	if err != nil {
		t.Fatalf("config.Parse rejected init's output: %v\n\n%s", err, out.YAML)
	}
	if len(cfg.Load.Stages) == 0 {
		t.Error("starter load: has no stages")
	}
	if len(cfg.Assert) == 0 {
		t.Error("starter assert: is empty — R-CFG-19 forbids this, and it is exactly the failure this requirement exists to prevent")
	}
	// A parse that only checks "err == nil" accepts an empty string for a
	// required field — that is exactly how an empty target.service slipped
	// through before. Assert the actual value, not just that Parse liked it.
	if cfg.Target.Service != "checkout-api" {
		t.Errorf("target.service = %q, want %q", cfg.Target.Service, "checkout-api")
	}
}

// spec: R-CLI-4
//
// When detection finds no build: service, buildInit must not write an
// empty (but present) service: field — that parses as non-empty YAML
// syntax while still failing config.Parse's emptiness check, and worse,
// looks like a complete file. It must instead leave the field out (a
// comment, like the unclassified-host convention above) and surface the
// gap, both in the file and via Gaps.
func TestBuildInitWithNoDetectedSUTDoesNotWriteEmptyServiceField(t *testing.T) {
	sys := &detect.System{} // SUT genuinely empty: no build: service found
	out := buildInit(sys, "./docker-compose.yml")
	content := string(out.YAML)

	if strings.Contains(content, "service: \n") || strings.Contains(content, "service:\n") {
		t.Errorf("wrote an empty service: field instead of omitting it:\n%s", content)
	}

	_, err := config.Parse(out.YAML)
	if err == nil {
		t.Fatal("config.Parse accepted a file with no target.service at all; want it to reject with a clear error, not silently pass")
	}
	if !strings.Contains(err.Error(), "target.service") {
		t.Errorf("config.Parse error = %q, want it to name target.service", err.Error())
	}

	found := false
	for _, g := range out.Gaps {
		if strings.Contains(g, "no system under test detected") {
			found = true
		}
	}
	if !found {
		t.Errorf("gaps = %v, want the missing SUT surfaced", out.Gaps)
	}
}

// spec: R-CLI-4
func TestBuildInitStarterDoesNotFabricateEndpoints(t *testing.T) {
	sys := &detect.System{SUT: "checkout-api"}
	out := buildInit(sys, "./docker-compose.yml")
	cfg, err := config.Parse(out.YAML)
	if err != nil {
		t.Fatalf("config.Parse rejected init's output: %v", err)
	}
	for _, sc := range cfg.Load.Scenarios {
		for _, step := range sc.Flow {
			if step.Path != "/" {
				t.Errorf("starter flow step invents an endpoint path %q; only \"/\" is permitted (R-CLI-4)", step.Path)
			}
		}
	}
}

// chdirTemp moves the test into an empty directory for the duration of the
// test, so `init --ci` writes into a scratch tree and not the repo.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// spec: R-CLI-11
//
// `--ci` with no provider writes the GitHub workflow. It is a mode, not a
// modifier: no detection runs (there is no compose file in this directory,
// and init must still succeed) and no torture.yaml is written.
func TestInitCIWritesGitHubWorkflowAndNotTortureYAML(t *testing.T) {
	dir := chdirTemp(t)

	var out, errb bytes.Buffer
	if code := runInit([]string{"-ci"}, &out, &errb); code != 0 {
		t.Fatalf("runInit -ci exited %d: %s", code, errb.String())
	}

	path := filepath.Join(dir, ".github", "workflows", "tortureu.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("workflow not written: %v", err)
	}
	if !strings.Contains(string(content), "tortureu run") {
		t.Errorf("workflow does not run tortureu:\n%s", content)
	}
	if _, err := os.Stat(filepath.Join(dir, "torture.yaml")); err == nil {
		t.Error("--ci wrote torture.yaml; R-CLI-11 makes it a mode that writes the pipeline only")
	}
	if !strings.Contains(out.String(), ".github/workflows/tortureu.yml") {
		t.Errorf("stdout does not name the file written:\n%s", out.String())
	}
}

// spec: R-CLI-11
func TestInitCIGitlabWritesGitlabPipeline(t *testing.T) {
	dir := chdirTemp(t)

	var out, errb bytes.Buffer
	if code := runInit([]string{"-ci", "gitlab"}, &out, &errb); code != 0 {
		t.Fatalf("runInit -ci gitlab exited %d: %s", code, errb.String())
	}
	content, err := os.ReadFile(filepath.Join(dir, ".gitlab-ci.yml"))
	if err != nil {
		t.Fatalf(".gitlab-ci.yml not written: %v", err)
	}
	if !strings.Contains(string(content), "tortureu run") {
		t.Errorf("pipeline does not run tortureu:\n%s", content)
	}
}

// spec: R-CLI-11
//
// R-DET-7 says gaps are surfaced, not hidden, and while no release is
// published the biggest gap in a generated pipeline is that it cannot yet
// install the binary it runs. The stdout report must say that in the same
// terms the file does — a red build for a stated reason — rather than
// leaving the user to discover it from a failing job.
func TestInitCIReportsTheInstallStateItActuallyGenerated(t *testing.T) {
	chdirTemp(t)
	var out, errb bytes.Buffer
	if code := runInit([]string{"-ci"}, &out, &errb); code != 0 {
		t.Fatalf("runInit -ci exited %d: %s", code, errb.String())
	}
	got := out.String()
	var want []string
	if ci.ReleaseVersion == "" {
		want = []string{"no published release", "exit 2"}
	} else {
		want = []string{ci.ReleaseVersion}
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("init --ci report does not mention %q (ReleaseVersion=%q):\n%s", w, ci.ReleaseVersion, got)
		}
	}
	if strings.Contains(got, "builds from source") {
		t.Errorf("init --ci still reports building from source:\n%s", got)
	}
}

// spec: R-CLI-11
//
// A pipeline file is hand-edited after generation — runner labels, secrets,
// the install step. Replacing it silently destroys work init cannot
// regenerate, so an existing file is a refusal, not an overwrite.
func TestInitCIRefusesToOverwriteAnExistingPipeline(t *testing.T) {
	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"-ci"}, filepath.Join(".github", "workflows", "tortureu.yml")},
		{[]string{"-ci", "gitlab"}, ".gitlab-ci.yml"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			dir := chdirTemp(t)
			full := filepath.Join(dir, tc.path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			mine := []byte("# hand-edited, do not clobber\n")
			if err := os.WriteFile(full, mine, 0o644); err != nil {
				t.Fatal(err)
			}

			var out, errb bytes.Buffer
			code := runInit(tc.args, &out, &errb)
			if code != 2 {
				t.Errorf("exit = %d, want 2 when the pipeline file already exists", code)
			}
			got, err := os.ReadFile(full)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(mine) {
				t.Errorf("existing pipeline was overwritten:\n%s", got)
			}
			if !strings.Contains(errb.String(), tc.path) {
				t.Errorf("stderr does not name the path it refused to write: %q", errb.String())
			}
		})
	}
}

// spec: R-CLI-11
func TestInitCIRejectsUnknownProviderListingSupportedOnes(t *testing.T) {
	chdirTemp(t)

	var out, errb bytes.Buffer
	code := runInit([]string{"-ci", "jenkins"}, &out, &errb)
	if code != 2 {
		t.Errorf("exit = %d, want 2 for an unknown CI provider", code)
	}
	for _, want := range []string{"github", "gitlab"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr %q does not list supported provider %q", errb.String(), want)
		}
	}
}

// spec: R-CLI-11 (-ci-out is the documented way to write somewhere else,
// which is what makes the refuse-to-overwrite rule livable)
func TestInitCIHonoursCIOut(t *testing.T) {
	dir := chdirTemp(t)

	var out, errb bytes.Buffer
	if code := runInit([]string{"-ci", "-ci-out", "ci/torture.yml"}, &out, &errb); code != 0 {
		t.Fatalf("runInit exited %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "ci", "torture.yml")); err != nil {
		t.Errorf("-ci-out path not written: %v", err)
	}
}

// spec: R-DC1-3
func TestInitDoesNotTouchAnotherToolsMCPRegistration(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(compose, []byte("services:\n  checkout-api:\n    build: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mcpFile := filepath.Join(dir, ".mcp.json")
	original := []byte(`{"mcpServers":{"k6":{}}}`)
	if err := os.WriteFile(mcpFile, original, 0o644); err != nil {
		t.Fatal(err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := runInit([]string{"-compose", "docker-compose.yml", "-out", "torture.yaml"}, &out, &errb)
	if code != 0 {
		t.Fatalf("runInit failed (exit %d): %s", code, errb.String())
	}
	got, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("init modified another tool's MCP registration file: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "torture.yaml")); err != nil {
		t.Errorf("torture.yaml was not written: %v", err)
	}
}

// spec: R-CLI-5
//
// R-CLI-5 was added after this task escalated that no requirement named a
// prerequisite preflight (previously cited here as "closest fit R-DET-7");
// re-pointed at the requirement written to describe it — "init MUST warn
// about any that are missing without failing."
//
// PATH is stubbed to a directory with nothing in it, so k6 (and docker) are
// genuinely absent via the real exec.LookPath, matching this task's
// instruction not to mock the check away. init must still succeed and
// still write the file — a config generated on a machine that cannot yet
// run it is still useful — and must name the missing tool in its warning.
func TestInitWarnsAboutMissingPrerequisiteButStillSucceeds(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(compose, []byte("services:\n  checkout-api:\n    build: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // no k6, no docker anywhere on PATH

	var out, errb bytes.Buffer
	code := runInit([]string{"-compose", "docker-compose.yml", "-out", "torture.yaml"}, &out, &errb)
	if code != 0 {
		t.Fatalf("runInit failed with a missing prerequisite (exit %d): %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "torture.yaml")); err != nil {
		t.Errorf("torture.yaml was not written despite a missing prerequisite: %v", err)
	}
	// docker, not k6: k6 is not required (R-CLI-5) because run executes it
	// in a container, so listing it under "missing what run needs" would
	// contradict its own hint.
	if !strings.Contains(out.String(), "docker") {
		t.Errorf("stdout does not name the missing required prerequisite:\n%s", out.String())
	}
	if strings.Contains(out.String(), "- k6:") {
		t.Errorf("stdout lists k6 as missing-and-needed; it is not required:\n%s", out.String())
	}
}

// spec: R-CLI-19
func TestBuildInitWritesBaseURLWhenTheSUTDeclaresExactlyOnePort(t *testing.T) {
	sys := &detect.System{SUT: "checkout-api", SUTPorts: []string{"8080"}}
	out := buildInit(sys, "./docker-compose.yml")
	content := string(out.YAML)

	if !strings.Contains(content, "base_url: http://localhost:8080") {
		t.Errorf("missing detected base_url:\n%s", content)
	}
	// The scheme is not detectable from compose, so it must be labelled.
	if !strings.Contains(content, "assum") {
		t.Errorf("base_url comment does not mark the scheme as an assumption:\n%s", content)
	}
	cfg, err := config.Parse([]byte(content))
	if err != nil {
		t.Fatalf("config.Parse rejected the generated file: %v\n%s", err, content)
	}
	if got, want := cfg.Target.BaseURL, "http://localhost:8080"; got != want {
		t.Errorf("parsed target.base_url = %q, want %q", got, want)
	}
}

// spec: R-CLI-19
//
// Several ports are a refusal, not a tie-break: immich's dev SUT declares
// 3000 and 24678, the second being Vite's HMR socket, so any "pick one"
// rule measures the file-watcher instead of the application.
func TestBuildInitRefusesToPickAmongSeveralPortsAndNamesThemAll(t *testing.T) {
	sys := &detect.System{SUT: "immich-web", SUTPorts: []string{"3000", "24678"}}
	out := buildInit(sys, "./docker-compose.yml")
	content := string(out.YAML)

	cfg, err := config.Parse([]byte(content))
	if err != nil {
		t.Fatalf("config.Parse rejected the generated file: %v\n%s", err, content)
	}
	if cfg.Target.BaseURL != "" {
		t.Errorf("base_url = %q, want empty — init must not pick among candidates", cfg.Target.BaseURL)
	}
	for _, want := range []string{"http://localhost:3000", "http://localhost:24678"} {
		if !strings.Contains(content, want) {
			t.Errorf("candidate %s not named in the generated file:\n%s", want, content)
		}
	}
	if !hasGapContaining(out.Gaps, "base_url") {
		t.Errorf("gaps = %v, want the undecided base_url surfaced", out.Gaps)
	}
}

// spec: R-CLI-19
func TestBuildInitReportsNoDeclaredPortWithoutInventingOne(t *testing.T) {
	sys := &detect.System{SUT: "checkout-api"}
	out := buildInit(sys, "./docker-compose.yml")
	content := string(out.YAML)

	cfg, err := config.Parse([]byte(content))
	if err != nil {
		t.Fatalf("config.Parse rejected the generated file: %v\n%s", err, content)
	}
	if cfg.Target.BaseURL != "" {
		t.Errorf("base_url = %q, want empty — nothing is declared to derive it from", cfg.Target.BaseURL)
	}
	if strings.Contains(content, "localhost:8080") {
		t.Errorf("init invented a base URL for a SUT that declares no port:\n%s", content)
	}
	if !hasGapContaining(out.Gaps, "base_url") {
		t.Errorf("gaps = %v, want the missing base_url surfaced", out.Gaps)
	}
}

// hasGapContaining reports whether any gap mentions sub.
func hasGapContaining(gaps []string, sub string) bool {
	for _, g := range gaps {
		if strings.Contains(g, sub) {
			return true
		}
	}
	return false
}

// spec: R-CLI-19
//
// End to end through the real verb, on the shape E1's control case has:
// "8081:8080" must yield the container port, since k6 dials base_url from
// inside the SUT's own network namespace.
func TestInitEmitsBaseURLFromTheContainerPortNotThePublishedHostPort(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "docker-compose.yml")
	body := "services:\n  checkout-api:\n    build: .\n    ports: [\"8081:8080\"]\n"
	if err := os.WriteFile(compose, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "torture.yaml")

	var out, errb bytes.Buffer
	if code := runInit([]string{"-compose", compose, "-out", outPath}, &out, &errb); code != 0 {
		t.Fatalf("runInit exit %d: %s", code, errb.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(got)
	if err != nil {
		t.Fatalf("config.Parse: %v\n%s", err, got)
	}
	if want := "http://localhost:8080"; cfg.Target.BaseURL != want {
		t.Errorf("target.base_url = %q, want %q\n%s", cfg.Target.BaseURL, want, got)
	}
}

// spec: R-DET-19
//
// A derived pick must not read as a detected fact: the file says it was
// derived from the depends_on graph and names the override, so a user whose
// front door is not the graph root can fix it in one flag.
func TestBuildInitMarksADerivedSUTAsAnAssumptionAndNamesTheOverride(t *testing.T) {
	sys := &detect.System{
		SUT:           "nginx",
		SUTChoice:     detect.SUTChoiceDerived,
		SUTCandidates: []string{"nginx", "web1", "web2"},
		SUTPorts:      []string{"80"},
	}
	out := buildInit(sys, "./docker-compose.yml")
	content := string(out.YAML)

	cfg, err := config.Parse([]byte(content))
	if err != nil {
		t.Fatalf("config.Parse rejected the generated file: %v\n%s", err, content)
	}
	if got, want := cfg.Target.Service, "nginx"; got != want {
		t.Errorf("target.service = %q, want %q", got, want)
	}
	if !strings.Contains(content, "-service") {
		t.Errorf("generated file does not name the -service override:\n%s", content)
	}
	for _, want := range []string{"web1", "web2"} {
		if !strings.Contains(content, want) {
			t.Errorf("generated file does not name the other candidate %s:\n%s", want, content)
		}
	}
}

// spec: R-DET-19
//
// The refusal case, mirroring R-CLI-19's for base_url: no service is named,
// every candidate is, and `-service` is the one-flag fix.
func TestBuildInitRefusesToNameAnUndecidedSUTAndReportsEveryCandidate(t *testing.T) {
	sys := &detect.System{
		SUTChoice:     detect.SUTChoiceUndecided,
		SUTCandidates: []string{"result", "vote", "worker"},
	}
	out := buildInit(sys, "./docker-compose.yml")
	content := string(out.YAML)

	if strings.Contains(content, "\n  service:") {
		t.Errorf("init named a service it did not decide on:\n%s", content)
	}
	for _, want := range []string{"result", "vote", "worker", "-service"} {
		if !strings.Contains(content, want) {
			t.Errorf("generated file does not name %s:\n%s", want, content)
		}
	}
	// The gap for this is detection's own (R-DET-19), reported to every
	// consumer rather than re-invented here, so buildInit must not print a
	// second copy of it.
	if hasGapContaining(out.Gaps, "system under test") {
		t.Errorf("gaps = %v, want detection's single report, not a duplicate", out.Gaps)
	}
}

// spec: R-DET-19
//
// The single-build-service case must stay silent: no assumption to disclose,
// so no noise about one.
func TestBuildInitSaysNothingAboutTheChoiceWhenThereWasOnlyOneBuildService(t *testing.T) {
	sys := &detect.System{SUT: "api", SUTChoice: detect.SUTChoiceOnly, SUTPorts: []string{"8080"}}
	content := string(buildInit(sys, "./docker-compose.yml").YAML)

	if strings.Contains(content, "-service") || strings.Contains(content, "depends_on") {
		t.Errorf("generated file discusses a choice that was never made:\n%s", content)
	}
}

// spec: R-DET-19
//
// End to end: -service settles the ambiguity without editing anything by
// hand, and the base_url follows the service the caller named.
func TestInitServiceFlagSettlesAnAmbiguousStack(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "docker-compose.yml")
	body := "services:\n" +
		"  frontend:\n    build: ./frontend\n    ports: [\"3000:3000\"]\n" +
		"  backend:\n    build: ./backend\n    ports: [\"8000:8000\"]\n"
	if err := os.WriteFile(compose, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "torture.yaml")

	var out, errb bytes.Buffer
	if code := runInit([]string{"-compose", compose, "-service", "backend", "-out", outPath}, &out, &errb); code != 0 {
		t.Fatalf("runInit exit %d: %s", code, errb.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(got)
	if err != nil {
		t.Fatalf("config.Parse: %v\n%s", err, got)
	}
	if cfg.Target.Service != "backend" {
		t.Errorf("target.service = %q, want backend\n%s", cfg.Target.Service, got)
	}
	if want := "http://localhost:8000"; cfg.Target.BaseURL != want {
		t.Errorf("target.base_url = %q, want %q", cfg.Target.BaseURL, want)
	}
}

// spec: R-DET-19
func TestInitFailsLoudlyWhenServiceFlagNamesNoComposeService(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(compose, []byte("services:\n  api:\n    build: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "torture.yaml")

	var out, errb bytes.Buffer
	code := runInit([]string{"-compose", compose, "-service", "nope", "-out", outPath}, &out, &errb)
	if code == 0 {
		t.Fatalf("runInit succeeded with a service that is not in the compose file: %s", out.String())
	}
	if !strings.Contains(errb.String(), "nope") || !strings.Contains(errb.String(), "api") {
		t.Errorf("stderr does not name both the bad service and the real one: %s", errb.String())
	}
	if _, err := os.Stat(outPath); err == nil {
		t.Errorf("torture.yaml was written despite the refusal")
	}
}
