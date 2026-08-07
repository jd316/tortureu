// Topology application is the executable run path R-DC2-7 requires before
// the DC-2 guarantee may be claimed anywhere: internal/egress.BuildTopology
// only produces the overlay's networks and the proxy's dual-homing (see its
// doc comment). Enforcement additionally requires the SUT's own compose
// services to be moved onto the internal-only network — otherwise they keep
// their default bridge network's route out and the overlay is decorative.
// This file does that: it parses the user's compose file to enumerate
// services (compose-go, already a project dependency — internal/detect uses
// it the same way) and writes an override compose file attaching every
// non-proxy service to the internal network, plus the proxy container
// itself dual-homed.
package run

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/compose-spec/compose-go/v2/loader"
	"gopkg.in/yaml.v3"

	"github.com/jdb316/tortureu/internal/egress"
)

// proxyImage is the TortureU egress proxy's container image. SPEC.md and
// RESEARCH.md name Toxiproxy as the fault-injection proxy (internal/fault's
// package doc, SPEC.md's R-EXE-15 table) but never pin an image reference or
// describe how its config maps host:port targets to individual Toxiproxy
// proxies — that wiring does not exist in any built package. Escalated in
// the Task 7 report: this constant is this package's own placeholder
// pending that decision, not a value SPEC.md specifies.
const proxyImage = "ghcr.io/shopify/toxiproxy:2.9.0"

// overlayNetwork/overlayService mirror the subset of docker-compose's schema
// this overlay needs. egress.Topology's own yaml tags can't be reused
// directly: its Service has no Image field, since BuildTopology only knows
// about network wiring, not container definitions (see the proxyImage
// comment above).
type overlayNetwork struct {
	Internal bool `yaml:"internal,omitempty"`
}

type overlayService struct {
	Image    string   `yaml:"image,omitempty"`
	Networks []string `yaml:"networks"`
}

type overlay struct {
	Networks map[string]overlayNetwork `yaml:"networks"`
	Services map[string]overlayService `yaml:"services"`
}

// ComposeTopologyApplier applies the R-DC2-3 overlay via `docker compose`
// (Up is the exec args used, injectable for tests that want to prove the
// override merges correctly without actually starting containers).
type ComposeTopologyApplier struct {
	// Bin is the docker binary; defaults to "docker".
	Bin string
	// Up is the docker-compose subcommand run against the merged files,
	// after "-f base -f overlay". Defaults to []string{"up", "-d", "--wait"}.
	// Tests substitute []string{"config"} to validate the merge without
	// starting any container.
	Up []string
}

func (a ComposeTopologyApplier) bin() string {
	if a.Bin != "" {
		return a.Bin
	}
	return "docker"
}

func (a ComposeTopologyApplier) up() []string {
	if a.Up != nil {
		return a.Up
	}
	return []string{"up", "-d", "--wait"}
}

// Apply enumerates composePath's services, writes an override file attaching
// every service except the proxy to top's internal network and the proxy
// container to both networks, then runs `docker compose -f composePath -f
// overlay <Up...>` (R-DC2-3, R-EXE-3: this must complete before the load
// generator's first request).
func (a ComposeTopologyApplier) Apply(composePath string, top egress.Topology) error {
	absPath, err := filepath.Abs(composePath)
	if err != nil {
		return err
	}
	workingDir := filepath.Dir(absPath)

	ctx := context.Background()
	configDetails, err := loader.LoadConfigFiles(ctx, []string{absPath}, workingDir)
	if err != nil {
		return fmt.Errorf("run: topology: load %s: %w", composePath, err)
	}
	project, err := loader.LoadWithContext(ctx, *configDetails, func(o *loader.Options) {
		o.SkipValidation = true
		o.SkipConsistencyCheck = true
	})
	if err != nil {
		return fmt.Errorf("run: topology: parse %s: %w", composePath, err)
	}

	var sutNetwork string
	for name, n := range top.Networks {
		if n.Internal {
			sutNetwork = name
			break
		}
	}
	if sutNetwork == "" {
		return fmt.Errorf("run: topology: BuildTopology produced no internal network")
	}

	var proxyName string
	var proxyNetworks []string
	for name, svc := range top.Services {
		proxyName = name
		proxyNetworks = svc.Networks
		break
	}
	if proxyName == "" {
		return fmt.Errorf("run: topology: BuildTopology produced no proxy service")
	}

	ov := overlay{
		Networks: map[string]overlayNetwork{},
		Services: map[string]overlayService{proxyName: {Image: proxyImage, Networks: proxyNetworks}},
	}
	for name, n := range top.Networks {
		ov.Networks[name] = overlayNetwork{Internal: n.Internal}
	}

	names := make([]string, 0, len(project.Services))
	for name := range project.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == proxyName {
			continue
		}
		ov.Services[name] = overlayService{Networks: []string{sutNetwork}}
	}

	out, err := yaml.Marshal(ov)
	if err != nil {
		return err
	}
	overlayPath := filepath.Join(os.TempDir(), "tortureu-topology-overlay.yaml")
	if err := os.WriteFile(overlayPath, out, 0o644); err != nil {
		return fmt.Errorf("run: topology: write overlay: %w", err)
	}

	args := append([]string{"compose", "-f", absPath, "-f", overlayPath}, a.up()...)
	cmd := exec.Command(a.bin(), args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("run: topology: %s %v: %w: %s", a.bin(), args, err, out)
	}
	return nil
}
