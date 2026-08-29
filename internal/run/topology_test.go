package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/egress"
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
//
// E1 found this against a real repo: R-EXE-20's rename trick disables the
// original internal-dependency service via a profile that is never
// activated, but compose treats a profile-disabled service that another
// service names in depends_on as undefined and refuses to start at all —
// `docker compose config` (this test's own validation mechanism, via
// ComposeTopologyApplier{Up: []string{"config"}}) errors with exactly
// "service ... depends on undefined service ...: invalid compose project"
// before this fix. E1 had to remove depends_on from its fixture to
// proceed; depends_on is an extremely common compose pattern, so this
// broke on real repos, not just edge cases.
//
// Confirmed empirically (outside this test, against real `docker compose
// config`) that simply adding a depends_on entry for the renamed backend
// is not enough: compose merges -f override files' depends_on maps
// additively by key, so the disabled original's key stays present in the
// merged result even when the override only adds a new one. The fix has
// to actually replace the service's depends_on, which is why Apply itself
// (not just this test) must succeed — a passing Apply() is the proof the
// merge-vs-replace distinction was handled, not just that some YAML was
// written.
func TestComposeTopologyApplier_RewritesDependsOnToBackendNameForInternalHost(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available")
	}

	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	compose := "services:\n  checkout-api:\n    image: alpine:3.20\n    depends_on:\n      - redis\n  redis:\n    image: redis:7-alpine\n"
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}

	top := egress.BuildTopology("tortureu_sut", "tortureu_egress", "tortureu-proxy")
	applier := ComposeTopologyApplier{Up: []string{"config"}}
	// Apply drives `docker compose ... config` as its own validation step
	// (a.up()); if the merged compose project is invalid (the
	// undefined-service error this fix addresses), Apply itself fails —
	// this is not a separate check bolted on afterward.
	if err := applier.Apply(composePath, top, nil, []string{"redis:6379"}); err != nil {
		t.Fatalf("Apply: %v — depends_on referencing the disabled original service was not rewritten to the backend clone", err)
	}

	out, err := exec.Command("docker", "compose", "-f", composePath, "-f", filepath.Join(os.TempDir(), "tortureu-topology-overlay.yaml"), "config").Output()
	if err != nil {
		t.Fatalf("docker compose config: %v", err)
	}
	merged := string(out)

	idx := strings.Index(merged, "checkout-api:")
	if idx == -1 {
		t.Fatal("merged config lost checkout-api")
	}
	section := merged[idx:]
	if next := strings.Index(section[1:], "\n  redis-tortureu-backend:"); next != -1 {
		section = section[:next+1]
	}
	if !strings.Contains(section, "depends_on:") || !strings.Contains(section, "redis-tortureu-backend") {
		t.Errorf("checkout-api's merged depends_on = %q, want it to depend on redis-tortureu-backend", section)
	}
	if strings.Contains(section, "\n      redis:\n") {
		t.Errorf("checkout-api's merged depends_on = %q, still references the disabled original \"redis\" — merging (rather than replacing) depends_on is exactly what leaves an undefined-service reference behind", section)
	}
}

// spec: R-EXE-20
//
// An E1 finding: R-EXE-20's rename trick disables an internal dependency's
// original service via an unused compose profile, but a profile-disabled
// service is not reliably removed by `docker compose down` — it leaked a
// container and its port into the next case's run. E1 worked around it in
// its own harness; the leak is this package's, because it reproduces
// identically for a real `tortureu run` user running twice in a row: the
// default reset command's own `docker compose up -d --wait` (run before
// Apply ever sees the overlay) starts the dependency undisabled, and
// nothing in the ordinary `down` path ever reaches it again once Apply's
// overlay disables it.
//
// This test reproduces that exact sequence for real, against a real Docker
// daemon: a vanilla `up` (no overlay — standing in for Reset's own command,
// which never mentions the overlay it doesn't yet know exists) starts
// "redis" undisabled; Apply then disables it via the profile and creates
// the backend clone; TeardownDisabled is asserted to remove the leaked
// "redis" container specifically, while leaving the rest of the stack
// (sut, backend) running — proving this is a scoped cleanup for the one
// leak this mechanism itself creates, not a wholesale teardown of
// Reset's own responsibility.
func TestComposeTopologyApplier_TeardownDisabledRemovesLeakedContainer(t *testing.T) {
	if err := exec.Command("docker", "compose", "version").Run(); err != nil {
		t.Skip("docker compose not available")
	}

	suffix := uniqueSuffix("tdt")
	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")
	compose := fmt.Sprintf("name: %sproj\nservices:\n  sut:\n    image: alpine:3.20\n    command: [\"sleep\", \"120\"]\n  redis:\n    image: redis:7-alpine\n", suffix)
	if err := os.WriteFile(composePath, []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(os.TempDir(), "tortureu-topology-overlay-"+suffix+".yaml")

	sutContainer := suffix + "proj-sut-1"
	redisContainer := suffix + "proj-redis-1"
	backendContainer := suffix + "proj-redis-tortureu-backend-1"
	proxyContainer := suffix + "proj-" + suffix + "-proxy-1"
	// The step-1 vanilla `up` (below) creates compose's own implicit
	// "default" network, since the base compose file declares no networks
	// of its own. Once Apply's overlay reassigns every service onto its
	// own named networks (sutNetwork/egressNetwork), nothing in the merged
	// config references "default" any more — `docker compose down` only
	// removes networks its *current* invocation's config still mentions,
	// so this one is silently orphaned rather than removed. Found the hard
	// way: leaking one per run exhausted this host's Docker network pool
	// ("all predefined address pools have been fully subnetted") under
	// -count=2, breaking unrelated tests — the exact "leftover state
	// poisons the next run" failure class this package exists to prevent
	// in the product, reappearing in its own test suite.
	defaultNetwork := suffix + "proj_default"
	t.Cleanup(func() {
		exec.Command("docker", "compose", "-f", composePath, "-f", overlayPath, "--profile", tortureuDisabledProfile, "down", "-v").Run()
		forceRemoveContainers(sutContainer, redisContainer, backendContainer, proxyContainer)
		forceRemoveNetworks(defaultNetwork, suffix+"_sut", suffix+"_egress")
	})

	// Step 1: the "before Apply ever runs" state — a plain `up`, exactly
	// what Reset's own default command does, using only the base compose
	// file (it has no idea the overlay path even exists yet).
	if out, err := exec.Command("docker", "compose", "-f", composePath, "up", "-d", "--wait").CombinedOutput(); err != nil {
		t.Fatalf("vanilla up: %v: %s", err, out)
	}
	if got := containerState(t, redisContainer, "{{.State.Running}}"); got != "true" {
		t.Fatalf("redis container not running after the vanilla up: %q", got)
	}

	// Step 2: Apply, exactly as Run calls it, with redis as an
	// internal-class fault target.
	top := egress.BuildTopology(suffix+"_sut", suffix+"_egress", suffix+"-proxy")
	// derivedPort, not the fixed default: this test runs `up` for real
	// (unlike most of this file's tests, which pass Up: []string{"config"}
	// to avoid needing real containers at all) — a fixed control port
	// would collide with itself under -count=2 or with any other
	// concurrently-running stack, exactly the class of bug
	// ProxyControlPort's own doc comment and dc2_enforcement_test.go's
	// derivedPort helper already exist to prevent.
	applier := ComposeTopologyApplier{OverlayPath: overlayPath, ProxyControlPort: derivedPort(suffix)}
	if err := applier.Apply(composePath, top, nil, []string{"redis:6379"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Confirm the leak actually reproduces before asserting the fix: the
	// original redis container from step 1 is still running, untouched by
	// Apply's own `up` (which excludes it via the disabling profile).
	if got := containerState(t, redisContainer, "{{.State.Running}}"); got != "true" {
		t.Fatalf("redis container = %q after Apply, want still running (reproducing the leak) — if this fails, the leak this test exists to catch may no longer reproduce the same way", got)
	}

	// Step 3: the fix.
	if err := applier.TeardownDisabled(composePath, []string{"redis:6379"}); err != nil {
		t.Fatalf("TeardownDisabled: %v", err)
	}

	if _, err := exec.Command("docker", "inspect", redisContainer).CombinedOutput(); err == nil {
		t.Errorf("container %s still exists after TeardownDisabled — the leaked disabled service was not removed", redisContainer)
	}
	// Scoped, not wholesale: the rest of the stack must still be running.
	if got := containerState(t, sutContainer, "{{.State.Running}}"); got != "true" {
		t.Errorf("sut container = %q after TeardownDisabled, want still running — this must not tear down the whole stack, only the leaked disabled service", got)
	}
	if got := containerState(t, backendContainer, "{{.State.Running}}"); got != "true" {
		t.Errorf("backend container = %q after TeardownDisabled, want still running", got)
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
