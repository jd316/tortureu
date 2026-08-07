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
	if err := applier.Apply(composePath, top, []string{"api.stripe.com:443"}, nil); err != nil {
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

// spec: R-EXE-20
func TestComposeTopologyApplier_RenamesInternalDependencyAndAliasesProxyToItsName(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available")
	}

	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	compose := "services:\n  checkout-api:\n    image: alpine:3.20\n  redis:\n    image: redis:7-alpine\n"
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	top := egress.BuildTopology("tortureu_sut", "tortureu_egress", "tortureu-proxy")
	applier := ComposeTopologyApplier{Up: []string{"config"}}
	if err := applier.Apply(composePath, top, nil, []string{"redis:6379"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	out, err := exec.Command("docker", "compose", "-f", composePath, "-f", filepath.Join(os.TempDir(), "tortureu-topology-overlay.yaml"), "config").Output()
	if err != nil {
		t.Fatalf("docker compose config: %v", err)
	}
	merged := string(out)

	// The original "redis" service must not start under its own name — a
	// SUT that resolves "redis" would otherwise reach the real container
	// directly, bypassing the proxy entirely, which is exactly the
	// aliasing-collision R-EXE-20 rules out. `docker compose config` itself
	// applies profile filtering the same way `up` does, so the disabled
	// service being entirely absent from `config`'s default-profile output
	// — not merely present-but-flagged — is the actual proof it won't
	// start; the raw overlay YAML (checked directly, not via `config`) is
	// what confirms *why*: a profiles: entry that isn't ever activated.
	overlayRaw, err := os.ReadFile(filepath.Join(os.TempDir(), "tortureu-topology-overlay.yaml"))
	if err != nil {
		t.Fatalf("read overlay: %v", err)
	}
	if !strings.Contains(string(overlayRaw), "tortureu-disabled") {
		t.Error("overlay does not disable the original redis service under an inactive profile")
	}
	if idx := strings.Index(merged, "\n  redis:\n"); idx != -1 {
		t.Errorf("merged config still starts the original \"redis\" service by default — it would keep claiming the DNS name directly, bypassing the proxy: %q", merged[idx:idx+80])
	}

	// The renamed backend must exist with the real image, reachable only on
	// the SUT network (so the proxy, not the SUT directly, can reach it).
	if !strings.Contains(merged, "redis-tortureu-backend") {
		t.Fatal("merged config has no renamed backend service for redis")
	}
	backendIdx := strings.Index(merged, "redis-tortureu-backend:")
	backendSection := merged[backendIdx:]
	if !strings.Contains(backendSection[:min(len(backendSection), 300)], "redis:7-alpine") {
		t.Error("renamed backend does not carry the original image")
	}

	// The proxy must claim the alias "redis" — the name the SUT already
	// resolves — on the SUT-side network.
	proxyIdx := strings.Index(merged, "tortureu-proxy:")
	if proxyIdx == -1 {
		t.Fatal("merged config lost the proxy service")
	}
	if !strings.Contains(merged[proxyIdx:], "redis") {
		t.Error("proxy service does not alias \"redis\" — the SUT has no path to it through the proxy")
	}
}

// spec: R-EXE-20
func TestComposeTopologyApplier_FailsLoudlyWhenInternalHostIsNotAKnownService(t *testing.T) {
	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte("services:\n  checkout-api:\n    image: alpine:3.20\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	top := egress.BuildTopology("tortureu_sut", "tortureu_egress", "tortureu-proxy")
	applier := ComposeTopologyApplier{Up: []string{"config"}}
	err := applier.Apply(composePath, top, nil, []string{"not-a-real-service:5432"})
	if err == nil {
		t.Fatal("Apply returned nil for an internal-class fault target with no matching compose service — R-EXE-20 requires failing loudly rather than running with the fault silently unintercepted")
	}
}
