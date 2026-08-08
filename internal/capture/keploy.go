package capture

// keploy.go is the `capture -engine keploy` delegate handoff (R-CLI-12).
//
// VERIFICATION STATUS (this file's keploy facts, checked 2026-08-08):
//
//	VERIFIED against the real keploy binary v3.6.11, downloaded from
//	https://github.com/keploy/keploy/releases/latest/download/keploy_linux_amd64.tar.gz
//	and run on this host:
//	  - `keploy record --help` prints exactly the flag names used below:
//	    -c/--command, --container-name, -n/--network-name, -p/--path,
//	    -b/--build-delay (default 30), --config-path (default "."),
//	    --record-timer, --generate-github-actions.
//	  - `keploy config --generate` writes a file named `keploy.yml` whose
//	    top-level keys include path, command, containerName, networkName,
//	    buildDelay, appName — the key spellings ConfigYAML below emits.
//	  - A PARTIAL keploy.yml is accepted: a file containing only
//	    command/containerName/buildDelay/path was read by
//	    `keploy record --config-path .`, which then got past its
//	    "missing required -c flag or appCmd in config file" validation
//	    (that error IS produced when the file is absent — control run).
//	    So the fragment this file emits is a usable config, not a guess.
//
//	PARTLY VERIFIED — the generated command was RUN against a real
//	two-service compose stack (a built `api` with `container_name:
//	kpdemo-api`, plus a redis dependency) on this host:
//	  - as an unprivileged user it stops at "failed setting up the
//	    environment: open /proc/sys/kernel/perf_event_paranoid: permission
//	    denied" — keploy's eBPF setup needs root;
//	  - as root it got past eBPF setup, wrote its own temporary compose
//	    file, started its `keploy-v3-*` agent container beside the stack,
//	    and created a container named exactly `kpdemo-api` — i.e. the
//	    --container-name this file derives from `container_name:` is the
//	    coupling keploy actually uses.
//
//	NOT VERIFIED: that a session is recorded end to end and testcases and
//	mocks are written. On this host the run then dies at
//	"mounts denied: the path <cwd>/keploy is not shared from the host" —
//	Docker Desktop file sharing refuses the output mount keploy needs, for
//	every path tried. That is this machine's Docker configuration, not the
//	generated command. Tracked as SPEC TBD-13.
//
// Nothing here runs keploy. `delegate` tier (R-SCOPE-3) means we generate
// the config and hand off; TortureU does not drive keploy on its clock and
// does not wrap its output.

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/jdb316/tortureu/internal/detect"
)

// KeployBinary is the name looked up on PATH. Presence is all that is
// checked, and presence is all that is claimed (R-CLI-5).
const KeployBinary = "keploy"

// KeployInstallHint is the official install source, quoted from keploy's
// README rather than inferred. TortureU never guesses an install location.
const KeployInstallHint = "install: curl --silent -O -L https://keploy.io/install.sh && source install.sh"

// KeployPlan is the generated handoff: what to run, what config to write,
// and what it will produce. It is data, not an execution — the caller
// prints it.
type KeployPlan struct {
	ComposePath   string // the compose file the plan was derived from
	SUT           string // compose service name of the system under test (R-DET-8)
	ContainerName string // that service's `container_name:`, never invented
	RecordCommand string // `keploy record ...`
	TestCommand   string // `keploy test ...`
	ConfigYAML    string // a keploy.yml fragment equivalent to RecordCommand
	Installed     bool   // keploy found on PATH
	InstallHint   string // set only when !Installed
}

// PlanKeploy derives the keploy handoff for the system described by
// composePath.
//
// It refuses rather than guesses (R-CLI-12). keploy record has exactly one
// hard requirement, -c/--command, and for a compose application it also
// needs --container-name to match the SUT service's `container_name:`.
// Compose invents a container name of its own (`<project>-<service>-1`)
// when the file states none, and that name depends on the project name,
// the compose version and the scale — so deriving it here would produce a
// keploy run that attaches to nothing and reports success. That failure is
// silent, which is precisely why this returns an error instead.
func PlanKeploy(composePath string) (KeployPlan, error) {
	sys, err := detect.Detect(composePath)
	if err != nil {
		return KeployPlan{}, fmt.Errorf("capture: keploy: could not detect the system from %s: %w", composePath, err)
	}
	if sys.SUT == "" {
		return KeployPlan{}, fmt.Errorf(
			"capture: keploy: no system under test was detected in %s (R-DET-8 identifies the SUT as the service with a `build:`); "+
				"keploy record needs an application to start, and TortureU will not guess which service that is",
			composePath)
	}

	containerName, err := composeContainerName(composePath, sys.SUT)
	if err != nil {
		return KeployPlan{}, err
	}
	if containerName == "" {
		return KeployPlan{}, fmt.Errorf(
			"capture: keploy: service %q in %s states no `container_name:`, so keploy's --container-name is unknown; "+
				"add `container_name: <name>` to that service and re-run. "+
				"Compose's own generated name depends on the project name and scale, and a wrong --container-name makes keploy record nothing while reporting success",
			sys.SUT, composePath)
	}

	abs, err := filepath.Abs(composePath)
	if err != nil {
		abs = composePath
	}
	appCmd := fmt.Sprintf("docker compose -f %s up", abs)

	plan := KeployPlan{
		ComposePath:   abs,
		SUT:           sys.SUT,
		ContainerName: containerName,
		// --build-delay is deliberately absent: its default is 30s and the
		// right value is however long this stack's image build takes,
		// which TortureU cannot know. The notes tell the user when to set it.
		RecordCommand: fmt.Sprintf("keploy record -c %q --container-name %s", appCmd, containerName),
		TestCommand:   fmt.Sprintf("keploy test -c %q --container-name %s", appCmd, containerName),
	}
	plan.ConfigYAML = keployConfigYAML(appCmd, containerName)
	plan.Installed = keployInstalled()
	if !plan.Installed {
		plan.InstallHint = KeployInstallHint
	}
	return plan, nil
}

// keployInstalled is a var so tests can observe both branches without
// depending on whether this machine happens to have keploy.
var keployInstalled = func() bool {
	_, err := exec.LookPath(KeployBinary)
	return err == nil
}

// keployConfigYAML emits the subset of keploy.yml that this handoff
// determines. It is a fragment, not a full config: keploy merges it over
// its own defaults (verified — see this file's header), and emitting the
// ~90 remaining default keys would mean restating values we did not
// choose and cannot keep in step with keploy's releases.
func keployConfigYAML(appCmd, containerName string) string {
	var b strings.Builder
	b.WriteString("# keploy.yml — generated by `tortureu capture -engine keploy`.\n")
	b.WriteString("# Fields keploy does not find here fall back to its own defaults;\n")
	b.WriteString("# run `keploy config --generate` for the full annotated file.\n")
	fmt.Fprintf(&b, "command: %q\n", appCmd)
	fmt.Fprintf(&b, "containerName: %q\n", containerName)
	b.WriteString("path: \"\"           # keploy writes ./keploy/ under this path (\"\" = cwd)\n")
	b.WriteString("networkName: \"\"    # set if your compose network is not keploy's default\n")
	b.WriteString("buildDelay: 30      # seconds keploy waits for the image build; raise it if the build is slower\n")
	return b.String()
}

// composeContainerName reads the `container_name:` of one service. It goes
// back to the compose file rather than to internal/detect because
// detect.System carries no container name — that package answers "what is
// this system made of", and adding a field to it for one delegate's flag
// is not this task's to do. Loading is cheap (one small file) and uses the
// same loader detect does, so the two cannot disagree about what the file
// says.
func composeContainerName(composePath, service string) (string, error) {
	abs, err := filepath.Abs(composePath)
	if err != nil {
		return "", fmt.Errorf("capture: keploy: %s: %w", composePath, err)
	}
	ctx := context.Background()
	details, err := loader.LoadConfigFiles(ctx, []string{abs}, filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("capture: keploy: read %s: %w", composePath, err)
	}
	project, err := loader.LoadWithContext(ctx, *details, func(o *loader.Options) {
		o.SkipValidation = true
		o.SkipConsistencyCheck = true
	})
	if err != nil {
		return "", fmt.Errorf("capture: keploy: parse %s: %w", composePath, err)
	}
	svc, ok := project.Services[service]
	if !ok {
		return "", fmt.Errorf("capture: keploy: service %q is not in %s", service, composePath)
	}
	return svc.ContainerName, nil
}

// KeployHandoff renders plan as the text the CLI prints. It states what
// keploy will produce and — always — that TortureU did not run it.
func KeployHandoff(plan KeployPlan) string {
	var b strings.Builder
	b.WriteString("tortureu capture -engine keploy: keploy is a delegate-tier tool (R-SCOPE-3).\n")
	b.WriteString("TortureU generated the command and config below and ran nothing. Traffic capture,\n")
	b.WriteString("test generation and mocking are keploy's, on keploy's clock.\n\n")

	fmt.Fprintf(&b, "detected system under test: service %q in %s (container %s)\n\n", plan.SUT, plan.ComposePath, plan.ContainerName)

	if !plan.Installed {
		fmt.Fprintf(&b, "keploy is not on PATH — %s\n", plan.InstallHint)
		b.WriteString("(keploy captures with eBPF: Linux kernel 5.10+, and it may ask for sudo.)\n")
		b.WriteString("The command below is still correct; it just needs keploy installed first.\n\n")
	}

	b.WriteString("run:\n")
	fmt.Fprintf(&b, "  %s\n\n", plan.RecordCommand)
	b.WriteString("then replay the recorded suite with:\n")
	fmt.Fprintf(&b, "  %s\n\n", plan.TestCommand)

	b.WriteString("this produces, under ./keploy/ :\n")
	b.WriteString("  <test-set>/tests/*.yaml   recorded request/response test cases\n")
	b.WriteString("  <test-set>/mocks.yaml     auto-generated dependency mocks\n")
	b.WriteString("  reports/                  results of `keploy test`\n\n")

	b.WriteString("equivalent keploy.yml (write it beside the compose file, or pass --config-path):\n")
	for _, line := range strings.Split(strings.TrimRight(plan.ConfigYAML, "\n"), "\n") {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	b.WriteString("\nnotes:\n")
	b.WriteString("  - keploy's cassettes are its own; they are not TortureU cassettes and\n")
	b.WriteString("    `tortureu replay` does not read them. -engine proxy is what writes those.\n")
	b.WriteString("  - if the image build takes longer than 30s, raise --build-delay.\n")
	return b.String()
}
