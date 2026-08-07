package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/egress"
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
		out, err := exec.Command("docker", "exec", stack.sut, "wget", "-T", "3", "-qO-", "http://fake-upstream:80/").CombinedOutput()
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

// dc2Stack is a running stack brought up through ComposeTopologyApplier.Apply.
type dc2Stack struct {
	sut, proxy, sutNet, egressNet string
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

// bringUpDC2Stack brings up a real compose stack (sut + a proxy dual-homed
// onto a real "upstream" container standing in for a classified external
// host) via ComposeTopologyApplier.Apply — the same call
// TestDC2Enforcement_... asserts against, and the same one
// removeIsolation's negative control starts from.
func bringUpDC2Stack(t *testing.T) dc2Stack {
	t.Helper()

	suffix := fmt.Sprintf("dc2t%d", time.Now().UnixNano()%1_000_000)
	sutNet := suffix + "_sut"
	egressNet := suffix + "_egress"
	proxyName := suffix + "-proxy"
	sutContainerName := suffix + "-sut-sut-1" // compose's default container-name shape: <project>-<service>-1
	proxyContainerName := suffix + "-sut-" + proxyName + "-1"
	upstreamContainer := suffix + "-upstream"

	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	compose := fmt.Sprintf("name: %s-sut\nservices:\n  sut:\n    image: alpine:3.20\n    command: [\"sleep\", \"300\"]\n", suffix)
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", upstreamContainer).Run()
		exec.Command("docker", "compose", "-f", composePath, "-f", filepath.Join(os.TempDir(), "tortureu-topology-overlay.yaml"), "down", "-v").Run()
		exec.Command("docker", "network", "rm", sutNet, egressNet).Run()
	})

	// The real upstream this classified host actually resolves to once past
	// the proxy — a stand-in for the real external service, reachable only
	// on the (non-internal) egress network, same as the real world.
	top := egress.BuildTopology(sutNet, egressNet, proxyName)
	applier := ComposeTopologyApplier{}
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
	toxi := &ToxiproxyApplier{BaseURL: "http://localhost:" + ProxyControlPort}
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

	return dc2Stack{sut: sut, proxy: proxy, sutNet: sutNet, egressNet: egressNet}
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
