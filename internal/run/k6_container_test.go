package run

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jd316/tortureu/internal/egress"
	"github.com/jd316/tortureu/internal/k6"
)

// spec: R-DC2-3
// spec: R-EXE-3
//
// This is the test the coordinator's review demanded and this package's own
// Docker tests were missing: everything in dc2_enforcement_test.go and
// internal_dep_interception_test.go drives the SUT from *inside* a
// container on its network (docker exec ... wget), which proves egress
// isolation but says nothing about whether the load generator — which runs
// from the host's perspective, dialing the SUT's own published port — can
// reach the SUT at all. It turned out it could not: R-DC2-3's own overlay
// puts the SUT on an internal:true network, and Docker does not publish
// host ports for a container whose only network is internal, so every k6
// request was connection-refused, on every run. This test brings up a real
// SUT through ComposeTopologyApplier — the exact production topology, SUT
// exclusively on the internal network, no port published for it at all —
// and drives it the way k6 actually does: from where the load generator
// runs, via K6Runner's own Start(), in container mode.
func TestK6Runner_ReachesInternalOnlySUTInContainerMode(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available")
	}

	suffix := uniqueSuffix("k6ct")
	sutNet := suffix + "_sut"
	egressNet := suffix + "_egress"
	proxyName := suffix + "-proxy"
	controlPort := derivedPort(suffix)
	sutContainerName := suffix + "-sut-sut-1"

	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	overlayPath := filepath.Join(os.TempDir(), "tortureu-topology-overlay-"+suffix+".yaml")
	// The SUT: a real nginx, on port 80, with NO ports: mapping at all —
	// exactly the shape the overlay produces for a real user's service.
	compose := fmt.Sprintf("name: %s-sut\nservices:\n  sut:\n    image: nginx:alpine\n", suffix)
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		exec.Command("docker", "compose", "-f", composePath, "-f", overlayPath, "down", "-v").Run()
		forceRemoveContainers(sutContainerName)
		forceRemoveNetworks(sutNet, egressNet)
	})

	top := egress.BuildTopology(sutNet, egressNet, proxyName)
	applier := ComposeTopologyApplier{ProxyControlPort: controlPort, OverlayPath: overlayPath}
	if err := applier.Apply(composePath, top, nil, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Confirm the setup actually reproduces the bug this fixes: the SUT's
	// port is genuinely not reachable from the host at all (nothing was
	// published for it — no port to even attempt).
	sut, err := findContainer(sutContainerName, "sut")
	if err != nil {
		t.Fatalf("find SUT container: %v", err)
	}
	if out, err := exec.Command("docker", "port", sut).CombinedOutput(); err == nil && strings.TrimSpace(string(out)) != "" {
		t.Fatalf("SUT has a published port (%s) — this test no longer exercises the internal-only-network case", out)
	}

	// Drive the SUT the way k6 does: from where the load generator runs,
	// via K6Runner.Start's real container-mode code path. Image/ContainerArgs
	// substitute a fake k6 (this package's own established pattern — see
	// fakeK6Script for host-process mode) so this test proves the real
	// docker-run/network-namespace/mount plumbing without needing the actual
	// k6 image; the property under test is reachability, not k6 itself.
	fakeScript := fmt.Sprintf(`set -e
echo "%s ramp_up 0"
echo "%s peak 1000"
wget -T 3 -qO /tmp/resp http://localhost:80/
echo '{"metrics":{"reach":{"values":{"ok":1}}}}' > %s
`, k6.PhaseMarkerPrefix, k6.PhaseMarkerPrefix, k6ContainerSummaryPath)

	r := &K6Runner{
		Dir:           dir,
		Image:         "alpine:3.20",
		ContainerArgs: []string{"sh", "-c", fakeScript},
	}
	r.SetSUTContainer(sut)

	handle, err := r.Start("// unused: ContainerArgs overrides the script invocation")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	var markers []string
	timeout := time.After(10 * time.Second)
collectMarkers:
	for {
		select {
		case m, ok := <-handle.Markers():
			if !ok {
				break collectMarkers
			}
			markers = append(markers, m.Phase)
		case <-timeout:
			t.Fatal("timed out waiting for phase markers")
		}
	}
	if len(markers) != 2 || markers[0] != "ramp_up" || markers[1] != "peak" {
		t.Errorf("markers = %v, want [ramp_up peak] — container-mode k6 stdout plumbing is not working", markers)
	}

	select {
	case result := <-handle.Done():
		var summary struct {
			Metrics struct {
				Reach struct {
					Values struct {
						OK int `json:"ok"`
					} `json:"values"`
				} `json:"reach"`
			} `json:"metrics"`
		}
		if err := json.Unmarshal(result.SummaryJSON, &summary); err != nil {
			t.Fatalf("unmarshal summary: %v: %s", err, result.SummaryJSON)
		}
		if summary.Metrics.Reach.Values.OK != 1 {
			t.Error("summary does not confirm the wget succeeded — the container-mode load generator did not actually reach the SUT")
		}
	case err := <-handle.Err():
		t.Fatalf("Err() = %v, want a result on Done() — this is exactly the R-DC2-3 bug (load generator could not reach the SUT) if it fires", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Done()")
	}
}

// spec: R-DC2-3
func TestDiscoverSUTContainer_FindsRealComposeContainer(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available")
	}

	suffix := uniqueSuffix("k6dt")
	sutNet := suffix + "_sut"
	egressNet := suffix + "_egress"
	proxyName := suffix + "-proxy"
	controlPort := derivedPort(suffix)
	sutContainerName := suffix + "-sut-sut-1"

	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	overlayPath := filepath.Join(os.TempDir(), "tortureu-topology-overlay-"+suffix+".yaml")
	compose := fmt.Sprintf("name: %s-sut\nservices:\n  sut:\n    image: alpine:3.20\n    command: [\"sleep\", \"120\"]\n", suffix)
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		exec.Command("docker", "compose", "-f", composePath, "-f", overlayPath, "down", "-v").Run()
		forceRemoveContainers(sutContainerName)
		forceRemoveNetworks(sutNet, egressNet)
	})

	top := egress.BuildTopology(sutNet, egressNet, proxyName)
	applier := ComposeTopologyApplier{ProxyControlPort: controlPort, OverlayPath: overlayPath}
	if err := applier.Apply(composePath, top, nil, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got, err := discoverSUTContainer("sut")
	if err != nil {
		t.Fatalf("discoverSUTContainer: %v", err)
	}
	// discoverSUTContainer returns a name (docker ps --format {{.Names}});
	// compare against the actual container's ID-independent identity via
	// `docker inspect`, rather than assuming another helper's return shape
	// (findContainer prefers returning an ID when its name guess matches).
	wantID, err := exec.Command("docker", "inspect", "-f", "{{.Id}}", sutContainerName).Output()
	if err != nil {
		t.Fatalf("docker inspect (reference): %v", err)
	}
	gotID, err := exec.Command("docker", "inspect", "-f", "{{.Id}}", got).Output()
	if err != nil {
		t.Fatalf("docker inspect (discovered %q): %v", got, err)
	}
	if string(gotID) != string(wantID) {
		t.Errorf("discoverSUTContainer = %q, which is a different container than the compose-created one", got)
	}
}
