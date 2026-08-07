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
func TestDC2Enforcement_InternalNetworkBlocksExternalButProxyForwardsClassifiedHost(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available")
	}

	suffix := fmt.Sprintf("dc2t%d", time.Now().UnixNano()%1_000_000)
	sutNet := suffix + "_sut"
	egressNet := suffix + "_egress"
	proxyName := suffix + "-proxy"
	sutContainer := suffix + "-sut-sut-1" // compose's default container-name shape: <project>-<service>-1
	upstreamContainer := suffix + "-upstream"
	proxyContainer := suffix + "-proxy-" + proxyName + "-1"

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

	sut, err := findContainer(sutContainer, "sut")
	if err != nil {
		t.Fatalf("find SUT container: %v", err)
	}

	// The classified host, dialed exactly as the SUT would dial it, must
	// reach the real upstream — through the proxy.
	out, err := exec.Command("docker", "exec", sut, "wget", "-T", "3", "-qO-", "http://fake-upstream:80/").CombinedOutput()
	if err != nil {
		t.Errorf("SUT could not reach the classified host through the proxy: %v: %s", err, out)
	} else if !strings.Contains(string(out), "nginx") && !strings.Contains(string(out), "html") {
		t.Errorf("unexpected response reaching the classified host: %q", out)
	}

	// An arbitrary external address — nothing classifies or proxies it —
	// must be unreachable: the internal:true network has no route out at
	// all (Docker's own enforcement, not a policy check this package could
	// get wrong).
	out, err = exec.Command("docker", "exec", sut, "wget", "-T", "3", "-qO-", "http://1.1.1.1/").CombinedOutput()
	if err == nil {
		t.Errorf("SUT reached an arbitrary external host — the internal SUT network is not actually isolated: %s", out)
	}

	_ = proxyContainer // documents the compose container-naming shape; not asserted on directly
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
