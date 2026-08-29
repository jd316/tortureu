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
const echoPort = "9000"
const echoTarget = "echo:9000"

func echoCommand() []string {
	encoded := base64.StdEncoding.EncodeToString([]byte(echoServerPy))
	script := "echo " + encoded + " | base64 -d | python3 -"
	return []string{"sh", "-c", script}
}

// mode names the three B2 configurations (BENCHMARKS.md §B2): the same
// scenario (client <-> echo, small round-trip payload) driven three ways.
type mode string

const (
	modeDirect        mode = "direct"    // client dials echo with no proxy on the path at all
	modeToxiproxy     mode = "toxiproxy" // client dials the real Toxiproxy proxy, no toxic installed
	modeOrchestrated  mode = "tortureu"  // same proxy path, but with a real (zero-effect) fault applied through fault.Translate + fault.Manager + ToxiproxyApplier -- the actual production orchestration path, not just the bare proxy
	orchestratedFault      = "b2_noop_orchestration"
)

// b2Stack is one running compose stack for one B2 configuration.
type b2Stack struct {
	echoContainer string
	clientCont    string
	manager       *fault.Manager
	toxi          *run.ToxiproxyApplier
	docker        run.DockerApplier
	combined      run.CombinedApplier
}

// withStack brings up a fresh, uniquely-suffixed stack for m, runs test
// against it, and ALWAYS tears it down afterward — cleanup is registered
// (via defer) before any docker command runs, mirroring b1's withStack and
// internal/run's own Docker-backed tests (dc2_enforcement_test.go,
// internal_dep_interception_test.go): a panic, a failed Apply, or a failed
// assertion inside test all still clean up.
func withStack(prefix string, m mode, test func(*b2Stack) result) result {
	suffix := uniqueSuffix(prefix)
	controlPort := derivedPort(suffix)
	sutNet := suffix + "_sut"
	egressNet := suffix + "_egress"
	proxyName := suffix + "-proxy"
	echoContainer := suffix + "-sut-echo-1"
	clientContainer := suffix + "-sut-client-1"
	proxyContainer := suffix + "-sut-" + proxyName + "-1"
	backendContainer := suffix + "-sut-echo-tortureu-backend-1"

	dir, err := os.MkdirTemp("", "b2-"+suffix)
	if err != nil {
		return result{unmeasuredNote: "MkdirTemp: " + err.Error()}
	}
	composePath := filepath.Join(dir, "docker-compose.yml")
	overlayPath := filepath.Join(os.TempDir(), "tortureu-b2-overlay-"+suffix+".yaml")

	defer func() {
		_ = exec.Command("docker", "compose", "-f", composePath, "-f", overlayPath, "down", "-v", "--remove-orphans").Run()
		forceRemoveContainers(backendContainer, proxyContainer, echoContainer, clientContainer)
		forceRemoveNetworks(sutNet, egressNet)
		_ = os.RemoveAll(dir)
		_ = os.Remove(overlayPath)
	}()

	cf := composeFile{
		Name: suffix + "-sut",
		Services: map[string]composeService{
			"echo":   {Image: echoImage, Command: echoCommand()},
			"client": {Image: clientImage, Command: []string{"sleep", "600"}},
		},
	}
	out, err := yaml.Marshal(cf)
	if err != nil {
		return result{unmeasuredNote: "marshal compose: " + err.Error()}
	}
	if err := os.WriteFile(composePath, out, 0o644); err != nil {
		return result{unmeasuredNote: "write compose: " + err.Error()}
	}

	intercept := m != modeDirect
	top := egress.BuildTopology(sutNet, egressNet, proxyName)
	applier := run.ComposeTopologyApplier{ProxyControlPort: controlPort, OverlayPath: overlayPath}
	var internalHosts []string
	if intercept {
		internalHosts = []string{echoTarget}
	}
	if err := applier.Apply(composePath, top, nil, internalHosts); err != nil {
		return result{unmeasuredNote: "Apply: " + err.Error()}
	}

	echoService, echoPreferred := "echo", echoContainer
	if intercept {
		echoService, echoPreferred = "echo-tortureu-backend", backendContainer
	}
	echoID, err := findContainer(echoPreferred, echoService)
	if err != nil {
		return result{unmeasuredNote: "find echo container: " + err.Error()}
	}
	clientID, err := findContainer(clientContainer, "client")
	if err != nil {
		return result{unmeasuredNote: "find client container: " + err.Error()}
	}

	toxi := &run.ToxiproxyApplier{BaseURL: "http://localhost:" + controlPort}
	docker := run.DockerApplier{}
	combined := run.CombinedApplier{Docker: docker, Toxiproxy: toxi}

	if intercept {
		upstream := backendServiceName("echo") + ":" + echoPort
		if err := waitFor(10*time.Second, func() error {
			return toxi.EnsureProxies(map[string]string{echoTarget: upstream})
		}); err != nil {
			return result{unmeasuredNote: "EnsureProxies: " + err.Error()}
		}
	}

	s := &b2Stack{echoContainer: echoID, clientCont: clientID, manager: &fault.Manager{}, toxi: toxi, docker: docker, combined: combined}
	defer s.manager.Teardown()

	if m == modeOrchestrated {
		// The actual production orchestration path: fault.Translate ->
		// fault.Manager.Apply -> ToxiproxyApplier, with a toxic that has no
		// observable effect (0ms latency, 0 jitter) -- this isolates
		// "TortureU's own orchestration is active" from "a fault is
		// actually distorting the traffic", which is a separate, already
		// well-measured question (B1).
		f := config.Fault{Name: orchestratedFault, Target: echoTarget, Verb: "latency", Inject: map[string]any{"latency": 0, "jitter": 0}}
		act, terr := fault.Translate(f)
		if terr != nil {
			return result{unmeasuredNote: "Translate(noop fault): " + terr.Error()}
		}
		toxi.RegisterTarget(f.Name, f.Target)
		if aerr := s.manager.Apply(s.combined, act); aerr != nil {
			return result{unmeasuredNote: "Apply(noop fault): " + aerr.Error()}
		}
	}

	readyScript := `import socket,sys
s=socket.socket()
s.settimeout(1)
s.connect((sys.argv[1], int(sys.argv[2])))
s.close()`
	if err := waitFor(10*time.Second, func() error {
		_, err := dockerExec(s.clientCont, "python3", "-c", readyScript, "echo", echoPort)
		return err
	}); err != nil {
		return result{unmeasuredNote: "echo service never became ready: " + err.Error()}
	}

	return test(s)
}

// runLoad execs loadClientPy inside the client container (the N-worker loop
// runs entirely inside the container, in one docker exec call, so
// process-spawn overhead is paid once, not per request) and parses its JSON
// summary.
func (s *b2Stack) runLoad(workers int, duration time.Duration) (loadResult, error) {
	out, err := dockerExec(s.clientCont, "python3", "-c", loadClientPy, "echo", echoPort, fmt.Sprint(workers), fmt.Sprintf("%.1f", duration.Seconds()))
	if err != nil {
		return loadResult{}, fmt.Errorf("exec: %w: %s", err, out)
	}
	obj, err := lastJSONObject(out)
	if err != nil {
		return loadResult{}, fmt.Errorf("parse: %w (%s)", err, out)
	}
	lr := loadResult{
		N:           int(obj["n"].(float64)),
		WallSeconds: obj["wall_seconds"].(float64),
	}
	if we, ok := obj["worker_errors"].(float64); ok {
		lr.WorkerErrors = int(we)
	}
	lr.LatenciesMs = float64s(obj["latencies_ms"])
	if lr.N == 0 || len(lr.LatenciesMs) == 0 {
		return lr, fmt.Errorf("no successful requests measured (worker_errors=%d)", lr.WorkerErrors)
	}
	return lr, nil
}

// generatorCeiling reads the load generator's OWN limits from inside the
// client container -- rule from BENCHMARKS.md §B2: "report the generator's
// own ceiling on the test machine (fd limit, ephemeral port range, CPU) ...
// a tool that reports 'your backend maxes at 2k rps' when the generator
// maxed out is worse than no tool." fd limit and ephemeral port range are
// read from the actual process doing the generating (inside the container),
// not the host, since that is the boundary that would actually bite first;
// CPU is the platform-wide figure already gathered by gatherPlatform.
func (s *b2Stack) generatorCeiling() generatorCeilingInfo {
	info := generatorCeilingInfo{}
	if out, err := dockerExec(s.clientCont, "sh", "-c", "ulimit -n"); err == nil {
		info.FDLimit = strings.TrimSpace(out)
	} else {
		info.FDLimit = "unmeasured: " + err.Error()
	}
	if out, err := dockerExec(s.clientCont, "cat", "/proc/sys/net/ipv4/ip_local_port_range"); err == nil {
		info.EphemeralPortRange = strings.TrimSpace(out)
	} else {
		info.EphemeralPortRange = "unmeasured: " + err.Error()
	}
	return info
}
