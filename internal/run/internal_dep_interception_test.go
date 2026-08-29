package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jd316/TortureU/internal/egress"
	"github.com/jd316/TortureU/internal/fault"
)

// spec: R-EXE-20
//
// This is the interception proof for the product's *primary* fault target,
// not the edge case DC-2 covers: torture.example.yaml's flagship faults
// (pg_slow, redis_dies) both target class: internal dependencies, and
// R-EXE-20 exists because aliasing the proxy to a dependency's own service
// name collides with that service's real DNS identity — the fix is moving
// the real dependency aside (backendServiceName) and giving the proxy the
// name the SUT already resolves.
//
// The positive case brings up a real stack through ComposeTopologyApplier
// (the same code path Run uses), applies a real Toxiproxy latency toxic,
// and measures the SUT's actual round-trip time to the dependency before
// and after — proving traffic really flows through the proxy, not just
// that Toxiproxy accepted the toxic. The negative control repeats the exact
// same fault against a second stack where Apply was never told about the
// internal host (no rename, no alias): the same toxic must have no
// observable effect there, since nothing put the proxy on the SUT's actual
// connection path.
func TestREXE20_LatencyOnInternalDependency_InterceptedVsNot(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available")
	}

	t.Run("intercepted: fault changes observed latency", func(t *testing.T) {
		measured := measureRedisLatencyWithFault(t, true)
		delta := measured.after - measured.before
		if delta < requestedLatencyFloor {
			t.Errorf("latency did not increase by at least %v (80%% of the requested %v fault): before=%v after=%v delta=%v — R-EXE-20's rename+alias mechanism is not actually on the SUT's connection path", requestedLatencyFloor, requestedLatency, measured.before, measured.after, delta)
		}
	})

	t.Run("negative control: without the rename, the same fault has no effect", func(t *testing.T) {
		measured := measureRedisLatencyWithFault(t, false)
		delta := measured.after - measured.before
		if delta >= requestedLatencyFloor {
			t.Errorf("latency increased by at least %v (80%% of the requested %v fault) even though the internal-host rename/alias was never applied — the SUT should be dialing the real redis directly, unaffected by an unaliased proxy: before=%v after=%v delta=%v", requestedLatencyFloor, requestedLatency, measured.before, measured.after, delta)
		}
	})
}

// requestedLatency is the toxic's own configured latency (see
// measureRedisLatencyWithFault's ApplyToxic call, "latency": 300); B1
// independently measures fidelity at +/-10ms, so this test only needs to
// prove interception happened, not re-measure precision.
//
// requestedLatencyFloor (80% of that) replaces a former `after <= before*2`
// ratio check: under system load the *baseline* ("before") measurement
// itself inflates (observed ~278ms against an idle-box baseline of a few
// ms), so a genuine ~490ms observation — a real, meaningful increase — fell
// short of doubling a noisy baseline. An absolute floor tied to what was
// actually requested is robust to baseline drift in a way a ratio never
// is: it only asks "did the delta reflect the fault," not "was the
// baseline itself small enough for a multiplier to work."
const (
	requestedLatency      = 300 * time.Millisecond
	requestedLatencyFloor = requestedLatency * 8 / 10
)

type latencyMeasurement struct{ before, after time.Duration }

// measureRedisLatencyWithFault brings up a fresh stack (sut + redis), and if
// intercept is true, applies R-EXE-20's rename+alias treatment to redis via
// ComposeTopologyApplier.Apply's internalHosts parameter — the same
// parameter Run passes for a class: internal, network-verb fault target.
// It measures the SUT's redis-cli round-trip time before and after adding a
// 300ms Toxiproxy latency toxic.
//
// Cleanup (compose-aware teardown plus a force-remove backstop) is
// registered before any docker command runs, so a failed assertion, a
// failed Apply, or a panic all still tear the stack down — a leaked
// container here is exactly what turned one transient failure into a
// permanently red suite (the coordinator's Defect 1).
func measureRedisLatencyWithFault(t *testing.T, intercept bool) latencyMeasurement {
	t.Helper()

	suffix := uniqueSuffix("r20t")
	controlPort := derivedPort(suffix)
	sutNet := suffix + "_sut"
	egressNet := suffix + "_egress"
	proxyName := suffix + "-proxy"
	sutContainer := suffix + "proj-sut-1"
	proxyContainer := suffix + "proj-" + proxyName + "-1"

	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	overlayPath := filepath.Join(os.TempDir(), "tortureu-topology-overlay-"+suffix+".yaml")

	t.Cleanup(func() {
		exec.Command("docker", "compose", "-f", composePath, "-f", overlayPath, "down", "-v").Run()
		forceRemoveContainers(proxyContainer, sutContainer)
		forceRemoveNetworks(sutNet, egressNet)
	})

	// The SUT image is redis:7-alpine purely to get redis-cli inside it —
	// it runs `sleep`, never its own redis-server, so it is genuinely
	// playing the SUT's role (a client dialing "redis"), not the dependency.
	compose := fmt.Sprintf("name: %sproj\nservices:\n  sut:\n    image: redis:7-alpine\n    command: [\"sleep\", \"300\"]\n  redis:\n    image: redis:7-alpine\n", suffix)
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	top := egress.BuildTopology(sutNet, egressNet, proxyName)
	applier := ComposeTopologyApplier{ProxyControlPort: controlPort, OverlayPath: overlayPath}
	var internalHosts []string
	if intercept {
		internalHosts = []string{"redis:6379"}
	}
	if err := applier.Apply(composePath, top, nil, internalHosts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	sut, err := findContainer(sutContainer, "sut")
	if err != nil {
		t.Fatalf("find SUT container: %v", err)
	}

	toxi := &ToxiproxyApplier{BaseURL: "http://localhost:" + controlPort}
	var undo func() error
	if intercept {
		upstream := backendServiceName("redis") + ":6379"
		if err := waitFor(5*time.Second, func() error {
			return toxi.EnsureProxies(map[string]string{"redis:6379": upstream})
		}); err != nil {
			t.Fatalf("EnsureProxies: %v", err)
		}
	}

	before := redisPingLatency(t, sut)

	toxi.RegisterTarget("r20_latency", "redis:6379")
	undo, err = toxi.ApplyToxic("r20_latency", fault.Toxic{
		Type:       "latency",
		Attributes: map[string]any{"latency": 300},
	})
	if err != nil {
		t.Fatalf("ApplyToxic: %v", err)
	}
	t.Cleanup(func() {
		if undo != nil {
			undo()
		}
	})

	after := redisPingLatency(t, sut)

	return latencyMeasurement{before: before, after: after}
}

// redisPingLatency execs redis-cli PING from inside the SUT container and
// returns the wall-clock round trip, measured from outside the container
// process (nanosecond epoch reads around the exec) rather than trusting any
// client-reported timing, so a proxy silently not being on the path can't
// hide behind a client library that doesn't notice.
func redisPingLatency(t *testing.T, sutContainer string) time.Duration {
	t.Helper()
	start := time.Now()
	out, err := exec.Command("docker", "exec", sutContainer, "redis-cli", "-h", "redis", "-p", "6379", "ping").CombinedOutput()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("redis-cli ping: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "PONG") {
		t.Fatalf("unexpected redis-cli ping response: %q", out)
	}
	return elapsed
}
