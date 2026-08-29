package run

import (
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jd316/tortureu/internal/egress"
)

// spec: R-DC2-3
// spec: R-DC2-7
//
// This is the test R-DC2-7 requires before the DC-2 guarantee may be
// claimed anywhere: it brings up a real compose stack through this
// package's own ComposeTopologyApplier.Apply (the exact code path Run uses,
// not a hand-rolled substitute), and proves the two things generation alone
// cannot: a container on the internal SUT network genuinely cannot reach
// the outside world, and a classified host is genuinely reachable — through
// the proxy, on the SUT-side network's DNS alias, exactly as the SUT would
// dial it.
//
// The negative-control subtest is what makes the positive assertion mean
// anything: it takes the identical stack Apply produced — same overlay,
// same proxy, same alias, same containers — and surgically recreates only
// the SUT-side network without Docker's own `internal: true` flag (which
// can't be toggled on an existing network, hence "recreate": disconnect,
// remove, create again with the one flag flipped, reconnect the same
// containers under the same aliases). It then asserts the external address
// IS reachable. Without this, a refactor that quietly stopped setting
// `internal: true` in the overlay (topology.go's ov.Networks[name] =
// overlayNetwork{Internal: n.Internal} silently receiving false) would
// leave the positive subtest green — the proxy path would still work, and
// nothing would notice the SUT regained a route to the internet, which is
// the exact failure DC-2 exists to prevent.
func TestDC2Enforcement_InternalNetworkBlocksExternalButProxyForwardsClassifiedHost(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available")
	}

	stack := bringUpDC2Stack(t)

	t.Run("isolated: external blocked, classified host reachable through the proxy", func(t *testing.T) {
		// Retry, do not single-shot: EnsureProxies returning means Toxiproxy
		// accepted the proxy definition, not that its upstream is serving yet.
		// A single attempt fails intermittently with "Connection reset by
		// peer" — observed on a GitHub runner, where the same commit passed on
		// the previous push. A flaky assertion about DC-2 is worse than a slow
		// one: it trains a reader to re-run a red build rather than read it.
		var out []byte
		err := waitFor(20*time.Second, func() error {
			var werr error
			out, werr = exec.Command("docker", "exec", stack.sut, "wget", "-T", "3", "-qO-", "http://fake-upstream:80/").CombinedOutput()
			return werr
		})
		if err != nil {
			t.Errorf("SUT could not reach the classified host through the proxy: %v: %s", err, out)
		} else if !strings.Contains(string(out), "nginx") && !strings.Contains(string(out), "html") {
			t.Errorf("unexpected response reaching the classified host: %q", out)
		}

		out, err = exec.Command("docker", "exec", stack.sut, "wget", "-T", "3", "-qO-", "http://1.1.1.1/").CombinedOutput()
		if err == nil {
			t.Errorf("SUT reached an arbitrary external host — the internal SUT network is not actually isolated: %s", out)
		}
	})

	t.Run("negative control: without internal:true, external IS reachable", func(t *testing.T) {
		stack.removeIsolation(t)

		out, err := exec.Command("docker", "exec", stack.sut, "wget", "-T", "3", "-qO-", "http://1.1.1.1/").CombinedOutput()
		if err != nil {
			t.Fatalf("SUT could not reach an arbitrary external host even with isolation removed: %v: %s — this means the positive subtest above could be passing for the wrong reason (a missing route or a DNS quirk unrelated to internal:true), not because DC-2 isolation actually works", err, out)
		}
	})
}

// spec: R-DC2-7
//
// A fixed Toxiproxy control port turned exactly one stray container (a
// manual probe, or a previous run's stack this suite failed to tear down)
// into a total suite failure: every subsequent stack, clean state or not,
// tried to bind that same host port and failed before any of its own
// assertions could run. bringUpDC2Stack now derives a per-run port from its
// own unique suffix; this proves two stacks can be genuinely up — bound,
// listening, answering — at the same time, not merely that they don't
// collide when carefully run one after another.
func TestDC2Enforcement_TwoStacksCoexistConcurrently(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available")
	}

	stackA := bringUpDC2Stack(t)
	stackB := bringUpDC2Stack(t)

	if stackA.controlPort == stackB.controlPort {
		t.Fatalf("both stacks derived the same control port %q — test setup did not actually exercise two independent ports", stackA.controlPort)
	}

	for i, s := range []dc2Stack{stackA, stackB} {
		out, err := exec.Command("docker", "exec", s.sut, "wget", "-T", "3", "-qO-", "http://fake-upstream:80/").CombinedOutput()
		if err != nil {
			t.Errorf("stack %d (control port %s): SUT could not reach the classified host through its own proxy while another stack was also up: %v: %s", i, s.controlPort, err, out)
		}
	}
}

// dc2Stack is a running stack brought up through ComposeTopologyApplier.Apply.
type dc2Stack struct {
	sut, proxy, sutNet, egressNet, controlPort string
}

// removeIsolation recreates s.sutNet without Docker's `internal: true` flag
// — the property can't be toggled on an existing network, so this
// disconnects every container from it, deletes it, recreates it as a
// normal (non-internal) bridge network under the identical name, and
// reconnects the same containers with the same aliases they had before.
// Nothing else about the stack changes: same proxy, same alias, same SUT
// container. This is the negative control for
// TestDC2Enforcement_InternalNetworkBlocksExternalButProxyForwardsClassifiedHost.
func (s dc2Stack) removeIsolation(t *testing.T) {
	t.Helper()
	run := func(args ...string) {
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
	}
	run("network", "disconnect", s.sutNet, s.sut)
	run("network", "disconnect", s.sutNet, s.proxy)
	run("network", "rm", s.sutNet)
	run("network", "create", s.sutNet) // no --internal: the one flag flipped
	run("network", "connect", s.sutNet, s.sut)
	run("network", "connect", "--alias", "fake-upstream", s.sutNet, s.proxy)
}

// suffixCounter guarantees uniqueSuffix never repeats within one test
// binary run even when called faster than the clock's resolution (e.g. two
// bringUpDC2Stack calls back to back in
// TestDC2Enforcement_TwoStacksCoexistConcurrently) — a repeated suffix would
// mean repeated network/container names and a repeated derived port,
// silently defeating the very isolation these tests exist to prove.
var suffixCounter atomic.Int64

// uniqueSuffix returns a short, practically-unique identifier for one test
// stack: prefix, a truncated nanosecond timestamp, and a monotonic counter.
func uniqueSuffix(prefix string) string {
	n := suffixCounter.Add(1)
	return fmt.Sprintf("%s%d%d", prefix, time.Now().UnixNano()%1_000_000, n)
}

// derivedPort computes a host port for one test stack's Toxiproxy control
// API from its own unique suffix, so two stacks — two subtests, two test
// runs, or a stack a previous run leaked and never cleaned up — do not
// contend for the fixed ProxyControlPort default (see its doc comment in
// topology.go). Range is chosen clear of IANA well-known ports and common
// dev-tool defaults; collisions with something else already bound there are
// still possible in principle but no more likely than for any other
// ephemeral-style port choice, and Apply's own error surfaces clearly if it
// happens (a bind failure, not a silent misconfiguration).
func derivedPort(suffix string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(suffix))
	return strconv.Itoa(20000 + int(h.Sum32()%20000))
}

// forceRemoveContainers force-removes containers by name, ignoring errors
// for names that never existed — a defensive backstop alongside `docker
// compose down`, registered before any container is actually created:
// `docker compose up` failing partway (e.g. this exact suite's fixed-port
// collision) can leave a container behind in a state `compose down` does
// not reliably clean up on its own, and a test that leaks a container is
// not reproducible — the next run inherits its failure for an unrelated
// reason (Defect 1 from the coordinator's review).
func forceRemoveContainers(names ...string) {
	args := append([]string{"rm", "-f"}, names...)
	_ = exec.Command("docker", args...).Run()
}

// forceRemoveNetworks removes networks by name, retrying briefly: a
// container disconnected moments earlier in the same cleanup can leave the
// network's endpoint list settling asynchronously, so an immediate
// `network rm` can transiently report "has active endpoints" even though
// nothing is really still attached.
func forceRemoveNetworks(names ...string) {
	for i := 0; i < 5; i++ {
		args := append([]string{"network", "rm"}, names...)
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "active endpoints") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// bringUpDC2Stack brings up a real compose stack (sut + a proxy dual-homed
// onto a real "upstream" container standing in for a classified external
// host) via ComposeTopologyApplier.Apply — the same call
// TestDC2Enforcement_... asserts against, and the same one
// removeIsolation's negative control starts from. Cleanup (both the
// compose-aware teardown and the force-remove backstop) is registered
// before any docker command runs, so it fires on success, on a failed
// assertion, on Apply itself failing, or on a panic — not only the happy
// path (R-EXE-5's own teardown-on-panic reasoning, applied to test
// infrastructure rather than a run).
func bringUpDC2Stack(t *testing.T) dc2Stack {
	t.Helper()

	suffix := uniqueSuffix("dc2t")
	controlPort := derivedPort(suffix)
	sutNet := suffix + "_sut"
	egressNet := suffix + "_egress"
	proxyName := suffix + "-proxy"
	sutContainerName := suffix + "-sut-sut-1" // compose's default container-name shape: <project>-<service>-1
	proxyContainerName := suffix + "-sut-" + proxyName + "-1"
	upstreamContainer := suffix + "-upstream"

	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	overlayPath := filepath.Join(os.TempDir(), "tortureu-topology-overlay-"+suffix+".yaml")

	// Registered first, before any resource exists: a force-remove backstop
	// that runs regardless of how or where this function or its caller
	// fails, plus the compose-aware teardown as the primary (cleaner) path.
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-f", composePath, "-f", overlayPath, "down", "-v").Run()
		forceRemoveContainers(upstreamContainer, proxyContainerName, sutContainerName)
		forceRemoveNetworks(sutNet, egressNet)
	})

	compose := fmt.Sprintf("name: %s-sut\nservices:\n  sut:\n    image: alpine:3.20\n    command: [\"sleep\", \"300\"]\n", suffix)
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	// The real upstream this classified host actually resolves to once past
	// the proxy — a stand-in for the real external service, reachable only
	// on the (non-internal) egress network, same as the real world.
	top := egress.BuildTopology(sutNet, egressNet, proxyName)
	applier := ComposeTopologyApplier{ProxyControlPort: controlPort, OverlayPath: overlayPath}
	if err := applier.Apply(composePath, top, []string{"fake-upstream:80"}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if out, err := exec.Command("docker", "run", "-d", "--name", upstreamContainer, "--network", egressNet, "nginx:alpine").CombinedOutput(); err != nil {
		t.Fatalf("docker run upstream: %v: %s", err, out)
	}
	upstreamIP, err := containerIP(upstreamContainer, egressNet)
	if err != nil {
		t.Fatalf("resolve upstream IP: %v", err)
	}

	// Eager creation (the review's Finding 1(a)): the proxy must exist
	// before load starts, not lazily when a fault first targets it.
	toxi := &ToxiproxyApplier{BaseURL: "http://localhost:" + controlPort}
	if err := waitFor(5*time.Second, func() error {
		return toxi.EnsureProxies(map[string]string{"fake-upstream:80": upstreamIP + ":80"})
	}); err != nil {
		t.Fatalf("EnsureProxies: %v", err)
	}

	sut, err := findContainer(sutContainerName, "sut")
	if err != nil {
		t.Fatalf("find SUT container: %v", err)
	}
	proxy, err := findContainer(proxyContainerName, proxyName)
	if err != nil {
		t.Fatalf("find proxy container: %v", err)
	}

	return dc2Stack{sut: sut, proxy: proxy, sutNet: sutNet, egressNet: egressNet, controlPort: controlPort}
}

func containerIP(name, network string) (string, error) {
	out, err := exec.Command("docker", "inspect", "-f",
		fmt.Sprintf("{{(index .NetworkSettings.Networks %q).IPAddress}}", network), name).Output()
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return "", fmt.Errorf("container %s has no address on network %s", name, network)
	}
	return ip, nil
}

// findContainer looks up the container compose actually created for
// service on a project it named after suffix, without hard-assuming
// compose's naming convention (which has changed across versions).
func findContainer(preferredName, service string) (string, error) {
	if out, err := exec.Command("docker", "inspect", "-f", "{{.Id}}", preferredName).Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	out, err := exec.Command("docker", "ps", "--filter", "label=com.docker.compose.service="+service, "--format", "{{.Names}}").Output()
	if err != nil {
		return "", err
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return "", fmt.Errorf("no running container found for service %q", service)
	}
	return names[0], nil
}

// waitFor retries fn until it succeeds or timeout elapses — EnsureProxies
// can race the proxy container's own startup (Apply's `--wait` covers the
// container reaching "healthy"/"running", not Toxiproxy's HTTP server
// inside it accepting connections yet).
func waitFor(timeout time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = fn(); lastErr == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return lastErr
}
