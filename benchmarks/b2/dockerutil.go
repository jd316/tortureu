// Generic docker/compose plumbing this harness needs to bring up and tear
// down real stacks through the SAME production code paths internal/run's own
// Docker-backed tests use (ComposeTopologyApplier.Apply, ToxiproxyApplier,
// DockerApplier). None of this is part of the path under test — it is the
// small set of helpers internal/run's own tests keep unexported, duplicated
// here (and again in benchmarks/b1, which cannot be imported either — it is
// also `package main`) rather than shortcut. See benchmarks/b1/dockerutil.go
// for the fuller rationale; this is the same pattern for a second benchmark
// binary.
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

// uniqueSuffix returns a short, practically-unique identifier for one run's
// stack, so two harness invocations (or a stack a crashed previous run
// leaked) never collide on container/network names or the derived control
// port.
func uniqueSuffix(prefix string) string {
	n := suffixCounter.Add(1)
	return fmt.Sprintf("%s%d%d", prefix, time.Now().UnixNano()%1_000_000, n)
}

// derivedPort computes a per-run Toxiproxy control-API host port from the
// stack's own unique suffix, so concurrent or leaked stacks don't contend
// for one fixed port.
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

// findContainer looks up the container compose actually created for
// service, without hard-assuming compose's container-naming convention.
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

// waitFor retries fn until it succeeds or timeout elapses.
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

// forceRemoveContainers is a defensive backstop alongside `docker compose
// down`, safe to call even for names that never existed.
func forceRemoveContainers(names ...string) {
	args := append([]string{"rm", "-f"}, names...)
	_ = exec.Command("docker", args...).Run()
}

// forceRemoveNetworks removes networks by name, retrying briefly past a
// transient "has active endpoints" while a just-disconnected container's
// endpoint settles.
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

// dockerExec runs `docker exec <container> args...` and returns combined
// output.
func dockerExec(container string, args ...string) (string, error) {
	full := append([]string{"exec", container}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	return string(out), err
}
