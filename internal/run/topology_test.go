package run

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/egress"
)

// spec: R-DC2-3
func TestComposeTopologyApplier_MergesInternalNetworkOntoSUTService(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available")
	}

	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("services:\n  checkout-api:\n    image: alpine:3.20\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	top := egress.BuildTopology("tortureu_sut", "tortureu_egress", "tortureu-proxy")
	applier := ComposeTopologyApplier{Up: []string{"config"}}
	if err := applier.Apply(composePath, top, []string{"api.stripe.com:443"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	out, err := exec.Command("docker", "compose", "-f", composePath, "-f", filepath.Join(os.TempDir(), "tortureu-topology-overlay.yaml"), "config").Output()
	if err != nil {
		t.Fatalf("docker compose config: %v", err)
	}
	merged := string(out)

	if !strings.Contains(merged, "tortureu_sut") {
		t.Error("merged config does not mention the internal SUT network")
	}
	if !strings.Contains(merged, "tortureu-proxy") {
		t.Error("merged config does not mention the proxy service — R-DC2-3 requires it be the SUT's only path off-box")
	}
	// checkout-api must have been reattached onto the internal network only
	// (BuildTopology alone only wires the proxy; see topology.go's doc
	// comment on why this package additionally enumerates the compose
	// services).
	idx := strings.Index(merged, "checkout-api:")
	if idx == -1 {
		t.Fatal("merged config lost the checkout-api service")
	}
	section := merged[idx:]
	// The next top-level (2-space-indented) service key ends checkout-api's
	// own block; docker compose config indents nested keys by >=4 spaces.
	if next := strings.Index(section[1:], "\n  tortureu-proxy:"); next != -1 {
		section = section[:next+1]
	}
	if !strings.Contains(section, "tortureu_sut") {
		t.Errorf("checkout-api's merged block = %q, want it to mention tortureu_sut — otherwise it keeps its default route out and R-DC2-3 is decorative", section)
	}

	// spec: R-DC2-3
	if !strings.Contains(merged, "api.stripe.com") {
		t.Error("merged config does not alias the classified external host to the proxy — without it, nothing tells the SUT to dial the proxy instead of nowhere")
	}
}
