// Generic docker/compose plumbing this harness needs to bring up and tear
// down real stacks through the SAME production code paths internal/run's own
// Docker-backed tests use (ComposeTopologyApplier.Apply, ToxiproxyApplier,
// DockerApplier). None of this is part of the fault path under test — it is
// the small set of helpers internal/run/dc2_enforcement_test.go and
// internal_dep_interception_test.go keep unexported (uniqueSuffix,
// derivedPort, findContainer, waitFor, containerIP, forceRemoveContainers/
// Networks, backendServiceName) that this package, being `package main`
// rather than an in-package `_test.go` file, cannot import and must
// reimplement. See BENCHMARKS.md's B1 task brief for why this is expected
// rather than a shortcut.
package main

import (
	"fmt"
	"hash/fnv"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var suffixCounter atomic.Int64

// uniqueSuffix mirrors dc2_enforcement_test.go's helper of the same name: a
// short, practically-unique identifier for one run's stack, so two harness
// invocations (or a stack a crashed previous run leaked) never collide on
// container/network names or the derived control port.
func uniqueSuffix(prefix string) string {
	n := suffixCounter.Add(1)
	return fmt.Sprintf("%s%d%d", prefix, time.Now().UnixNano()%1_000_000, n)
}

// derivedPort mirrors dc2_enforcement_test.go's helper: a per-run Toxiproxy
// control-API host port derived from the stack's own unique suffix, so
// concurrent or leaked stacks don't contend for one fixed port.
func derivedPort(suffix string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(suffix))
	return strconv.Itoa(20000 + int(h.Sum32()%20000))
}

// backendServiceName mirrors internal/run/topology.go's unexported helper of
// the same name: the name R-EXE-20's rename trick gives the real dependency
// once the proxy has taken over its original DNS name.
func backendServiceName(hostname string) string {
	return hostname + "-tortureu-backend"
}

// findContainer mirrors dc2_enforcement_test.go's helper: look up the
// container compose actually created for service, without hard-assuming
// compose's container-naming convention.
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

// waitFor mirrors dc2_enforcement_test.go's helper: retries fn until it
// succeeds or timeout elapses.
func waitFor(timeout time.Duration, fn func() error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = fn(); lastErr == nil {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return lastErr
}

// forceRemoveContainers mirrors dc2_enforcement_test.go's helper: a
// defensive backstop alongside `docker compose down`, safe to call even for
// names that never existed.
func forceRemoveContainers(names ...string) {
	args := append([]string{"rm", "-f"}, names...)
	_ = exec.Command("docker", args...).Run()
}

// forceRemoveNetworks mirrors dc2_enforcement_test.go's helper: removes
// networks by name, retrying briefly past a transient "has active
// endpoints" while a just-disconnected container's endpoint settles.
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

// dockerInspect runs `docker inspect -f <format> <container>` and returns
// the trimmed output — used by the kill measurement (R-EXE-25) to read the
// container's own recorded exit status/code directly, rather than inferring
// it from client-visible TCP behavior.
func dockerInspect(container, format string) (string, error) {
	out, err := exec.Command("docker", "inspect", "-f", format, container).Output()
	return strings.TrimSpace(string(out)), err
}

// dockerExec runs `docker exec <container> args...` and returns combined
// output.
func dockerExec(container string, args ...string) (string, error) {
	full := append([]string{"exec", container}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	return string(out), err
}

// dockerExecDetached runs `docker exec -d <container> args...` (fire and
// forget, used for the pause/kill background pinger).
func dockerExecDetached(container string, args ...string) error {
	full := append([]string{"exec", "-d", container}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker exec -d: %w: %s", err, out)
	}
	return nil
}
