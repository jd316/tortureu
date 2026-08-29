package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/egress"
)

// spec: R-DC2-3
//
// The overlay reproduces each internal dependency as a "-tortureu-backend"
// service so the proxy can take over its DNS name. It copied only `image:`,
// so a dependency built from source — an ordinary compose shape, and what
// examples/quickstart uses — produced a service with neither an image nor a
// build context, and compose rejected the entire project:
//
//	service "dep-tortureu-backend" has neither an image nor a build context
//	specified: invalid compose project
//
// Uses Up: ["config"] so compose validates the merge without starting
// anything, which is also the strongest form of this assertion: compose
// itself is the judge of whether the overlay is a valid project.
func TestOverlayBackend_CarriesABuiltDependencysContext(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"api", "dep"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sub, "Dockerfile"), []byte("FROM alpine:3.20\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	compose := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(compose, []byte(`services:
  api:
    build: ./api
  dep:
    build: ./dep
`), 0o644); err != nil {
		t.Fatal(err)
	}

	overlayPath := filepath.Join(dir, "overlay.yaml")
	a := ComposeTopologyApplier{Up: []string{"config"}, OverlayPath: overlayPath}
	err := a.Apply(compose, egress.BuildTopology(sutNetworkName, egressNetworkName, proxyServiceName), nil, []string{"dep"})

	raw, rerr := os.ReadFile(overlayPath)
	if rerr != nil {
		t.Fatalf("overlay was not written: %v (Apply: %v)", rerr, err)
	}
	overlay := string(raw)
	if !strings.Contains(overlay, "dep-tortureu-backend") {
		t.Fatalf("overlay has no backend service:\n%s", overlay)
	}
	if !strings.Contains(overlay, "build:") {
		t.Errorf("backend for a built dependency declares no build context:\n%s", overlay)
	}
	// compose is the real judge: `config` fails on an invalid project.
	if err != nil && strings.Contains(err.Error(), "neither an image nor a build context") {
		t.Fatalf("compose rejected the overlay: %v", err)
	}
}
