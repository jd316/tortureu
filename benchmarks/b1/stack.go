package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/egress"
	"github.com/jd316/TortureU/internal/fault"
	"github.com/jd316/TortureU/internal/run"
)

// composeService/composeFile are the minimal docker-compose shapes this
// harness needs to author its own base compose file (distinct from
// internal/run/topology.go's overlay, which merges ON TOP of a file like
// this one — exactly as ComposeTopologyApplier.Apply expects).
type composeService struct {
	Image   string   `yaml:"image"`
	Command []string `yaml:"command,omitempty"`
}

type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
}

const echoImage = "python:3-alpine"
const clientImage = "python:3-alpine"

// cpuEchoImage is the local image tag the cpu verb's test builds once,
// ahead of any stack coming up, with stress-ng already baked in. It CANNOT
// be installed at container start via `apk add` the way an ordinary
// Dockerfile RUN would: ComposeTopologyApplier.Apply moves every service in
// the stack — not just the fault's target — onto the R-DC2-3 internal-only
// network (that isolation is the whole point of DC-2), so by the time the
// echo container's own startup command runs, it already has no route to an
// Alpine mirror. Baking the package into an image on the host, before Apply
// ever runs, sidesteps that entirely: the image pull/build uses the host's
// normal network, and the resulting container needs no runtime network
// access to already contain stress-ng.
const cpuEchoImage = "tortureu-b1-echo-cpu:latest"

// buildCPUEchoImage builds cpuEchoImage once (idempotent: re-running `docker
// build` on an unchanged Dockerfile is a cache hit).
func buildCPUEchoImage() error {
	dockerfile := "FROM " + echoImage + "\nRUN apk add --no-cache stress-ng\n"
	cmd := exec.Command("docker", "build", "-t", cpuEchoImage, "-f", "-", ".")
	cmd.Stdin = strings.NewReader(dockerfile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker build %s: %w: %s", cpuEchoImage, err, out)
	}
	return nil
}

// echoCommand builds the shell command that starts echoServerPy, base64-
// encoded directly into the command line rather than passed through an
// environment variable or a literal "$"-bearing shell snippet.
//
// This is NOT just paranoia: ComposeTopologyApplier.Apply (topology.go)
// parses the base compose file once itself, via compose-go's loader, to
// enumerate services and clone R-EXE-20 "internal host" services into a
// backendServiceName clone in the overlay it writes — and compose-go's own
// loader performs variable interpolation ($$-unescaping included) at that
// point. The overlay file it writes is then interpolated a SECOND time by
// the real `docker compose ... up` CLI invocation. A `$$ECHO_PY` escape
// meant to survive exactly one interpolation pass (plain docker compose)
// instead survives zero: the first (in-process) pass unescapes it to
// `$ECHO_PY`, and the second (CLI) pass then substitutes it against the
// HOST's environment, where it's unset — silently emptying the script and
// making the container exit(0) immediately. Base64 has no "$" in its
// alphabet, so it survives any number of interpolation passes unchanged.
func echoCommand() []string {
	encoded := base64.StdEncoding.EncodeToString([]byte(echoServerPy))
	script := "echo " + encoded + " | base64 -d | python3 -"
	return []string{"sh", "-c", script}
}

// b1Stack is one running compose stack: a client and an echo container,
// optionally fronted by a real Toxiproxy proxy on the client's actual
// connection path to echo (R-EXE-20's rename+alias mechanism, via
// ComposeTopologyApplier.Apply — the same call internal/run's own tests use).
type b1Stack struct {
	suffix      string
	composePath string
	overlayPath string
	controlPort string
	sutNet      string
	egressNet   string
	proxyName   string

	echoContainer string
	clientCont    string

	toxi     *run.ToxiproxyApplier
	docker   run.DockerApplier
	combined run.CombinedApplier
	manager  *fault.Manager

	intercepted bool
}

// echoTarget is the host:port the client dials for echo — always "echo:9000"
// whether or not a proxy is on the path (R-EXE-20's alias makes that
// transparent to the dialer).
const echoTarget = "echo:9000"
const echoPort = "9000"

// withStack brings up one fresh, uniquely-suffixed stack, runs test against
// it, and ALWAYS tears it down afterward — cleanup is registered (via defer)
// before any docker command runs, so a panic inside test, a failed Apply, or
// a failed assertion all still clean up, mirroring
// dc2_enforcement_test.go/internal_dep_interception_test.go's
// t.Cleanup-first ordering with plain deferred funcs since this isn't a
// *testing.T context.
func withStack(prefix string, intercept bool, echoImageOverride string, test func(*b1Stack) Result) Result {
	suffix := uniqueSuffix(prefix)
	controlPort := derivedPort(suffix)
	sutNet := suffix + "_sut"
	egressNet := suffix + "_egress"
	proxyName := suffix + "-proxy"
	echoContainer := suffix + "-sut-echo-1"
	clientContainer := suffix + "-sut-client-1"
	proxyContainer := suffix + "-sut-" + proxyName + "-1"
	backendContainer := suffix + "-sut-echo-tortureu-backend-1"

	dir, err := os.MkdirTemp("", "b1-"+suffix)
	if err != nil {
		return Result{Verdict: "unmeasured", Notes: []string{"MkdirTemp: " + err.Error()}}
	}
	composePath := filepath.Join(dir, "docker-compose.yml")
	overlayPath := filepath.Join(os.TempDir(), "tortureu-b1-overlay-"+suffix+".yaml")

	// Registered first, before any docker resource exists (rule 4): a
	// force-remove backstop alongside the compose-aware teardown, so this
	// fires on success, on a failed assertion, on Apply failing, or on a
	// panic anywhere in test.
	defer func() {
		_ = exec.Command("docker", "compose", "-f", composePath, "-f", overlayPath, "down", "-v", "--remove-orphans").Run()
		forceRemoveContainers(backendContainer, proxyContainer, echoContainer, clientContainer)
		forceRemoveNetworks(sutNet, egressNet)
		_ = os.RemoveAll(dir)
		_ = os.Remove(overlayPath)
	}()

	img := echoImage
	if echoImageOverride != "" {
		img = echoImageOverride
	}
	cf := composeFile{
		Name: suffix + "-sut",
		Services: map[string]composeService{
			"echo": {
				Image:   img,
				Command: echoCommand(),
			},
			"client": {
				Image:   clientImage,
				Command: []string{"sleep", "600"},
			},
		},
	}
	out, err := yaml.Marshal(cf)
	if err != nil {
		return Result{Verdict: "unmeasured", Notes: []string{"marshal compose: " + err.Error()}}
	}
	if err := os.WriteFile(composePath, out, 0o644); err != nil {
		return Result{Verdict: "unmeasured", Notes: []string{"write compose: " + err.Error()}}
	}

	top := egress.BuildTopology(sutNet, egressNet, proxyName)
	applier := run.ComposeTopologyApplier{ProxyControlPort: controlPort, OverlayPath: overlayPath}
	var internalHosts []string
	if intercept {
		internalHosts = []string{echoTarget}
	}
	if err := applier.Apply(composePath, top, nil, internalHosts); err != nil {
		return Result{Verdict: "unmeasured", Notes: []string{"Apply: " + err.Error()}}
	}

	echoService := "echo"
	echoPreferred := echoContainer
	if intercept {
		// R-EXE-20 disables the real "echo" service and clones its
		// definition under backendServiceName("echo") instead.
		echoService = "echo-tortureu-backend"
		echoPreferred = backendContainer
	}
	echoID, err := findContainer(echoPreferred, echoService)
	if err != nil {
		return Result{Verdict: "unmeasured", Notes: []string{"find echo container: " + err.Error()}}
	}
	clientID, err := findContainer(clientContainer, "client")
	if err != nil {
		return Result{Verdict: "unmeasured", Notes: []string{"find client container: " + err.Error()}}
	}

	toxi := &run.ToxiproxyApplier{BaseURL: "http://localhost:" + controlPort}
	docker := run.DockerApplier{}
	combined := run.CombinedApplier{Docker: docker, Toxiproxy: toxi}

	if intercept {
		upstream := backendServiceName("echo") + ":" + echoPort
		if err := waitFor(10*time.Second, func() error {
			return toxi.EnsureProxies(map[string]string{echoTarget: upstream})
		}); err != nil {
			return Result{Verdict: "unmeasured", Notes: []string{"EnsureProxies: " + err.Error()}}
		}
	}

	s := &b1Stack{
		suffix: suffix, composePath: composePath, overlayPath: overlayPath,
		controlPort: controlPort, sutNet: sutNet, egressNet: egressNet, proxyName: proxyName,
		echoContainer: echoID, clientCont: clientID,
		toxi: toxi, docker: docker, combined: combined, manager: &fault.Manager{},
		intercepted: intercept,
	}
	defer s.manager.Teardown()

	// Confirm the echo service is actually accepting connections before any
	// measurement runs (compose --wait covers container health, not this
	// application-level readiness).
	readyScript := `import socket,sys
s=socket.socket()
s.settimeout(1)
s.connect((sys.argv[1], int(sys.argv[2])))
s.close()`
	if err := waitFor(10*time.Second, func() error {
		_, err := dockerExec(s.clientCont, "python3", "-c", readyScript, "echo", echoPort)
		return err
	}); err != nil {
		return Result{Verdict: "unmeasured", Notes: []string{"echo service never became ready: " + err.Error()}}
	}

	return test(s)
}

// applyFault runs f through the REAL fault.Translate -> fault.Manager.Apply
// path against s's combined applier, registering the Toxiproxy target first
// exactly as internal/run's scheduler does (see ToxiproxyApplier's package
// doc for why RegisterTarget must run before Apply). Returns the translated
// Action (stringified, for the results file) and any error from Translate or
// Apply — the primary thing this whole benchmark exists to observe.
func (s *b1Stack) applyFault(f config.Fault) (translated string, err error) {
	act, terr := fault.Translate(f)
	if terr != nil {
		return "", fmt.Errorf("Translate: %w", terr)
	}
	translated = fmt.Sprintf("%+v", act)
	if act.Kind == fault.KindToxic {
		s.toxi.RegisterTarget(f.Name, f.Target)
	}
	if aerr := s.manager.Apply(s.combined, act); aerr != nil {
		return translated, fmt.Errorf("Apply: %w", aerr)
	}
	return translated, nil
}
