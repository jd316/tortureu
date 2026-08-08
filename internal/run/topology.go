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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
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

// ProxyControlPort is the default host port the R-DC2-3 overlay publishes
// the proxy's Toxiproxy control API on (container port 8474, Toxiproxy's
// own default control port). Whatever runs internal/run — the orchestrator
// — needs to reach that API to create proxies/toxics, and typically runs on
// the developer's host, outside every Docker network this package creates;
// a published host port is how it gets there.
//
// This remains a genuine v0 limitation for *production* `tortureu run`:
// NewRealDeps constructs ToxiproxyApplier.BaseURL before Run ever calls
// Apply, so the port has to be known ahead of time rather than discovered
// from an ephemeral one Apply picks — the same reasoning as this package's
// other fixed naming conventions (sutNetworkName etc. in run.go). SPEC.md
// does not specify this either; escalated in the Task 7 report.
//
// It is NOT a limitation for anything that can derive its own per-run
// identifier and pass it as ComposeTopologyApplier.ProxyControlPort instead
// — this package's own Docker-backed tests do exactly that (see
// derivedPort in dc2_enforcement_test.go), and
// TestDC2Enforcement_TwoStacksCoexistConcurrently proves two independently
// configured stacks can be up at the same time without contending for this
// port. A stray container left over from a previous, uncleaned run (this
// package's tests leaking, or a manual `docker run` probe) previously
// turned into a total suite failure for exactly this reason: every run,
// clean or not, always tried to bind the same fixed port.
const ProxyControlPort = "18474"

// overlayNetwork/overlayService mirror the subset of docker-compose's schema
// this overlay needs. egress.Topology's own yaml tags can't be reused
// directly: its Service has no Image field, since BuildTopology only knows
// about network wiring, not container definitions (see the proxyImage
// comment above).
// Name is set explicitly (rather than left to compose's own default of
// prefixing every network with the project name) so sutNetworkName and
// egressNetworkName in run.go are the actual Docker network names — needed
// for anything outside this compose invocation (this package's own tests,
// or a future capture/replay component) to find them by that fixed name.
type overlayNetwork struct {
	Name     string `yaml:"name,omitempty"`
	Internal bool   `yaml:"internal,omitempty"`
}

// overlayService's Networks is `any` because compose's own schema allows two
// shapes for a service's networks: key, and this overlay needs both. Plain
// SUT services get the short list form (`networks: [name]`). The proxy
// needs the long per-network form so it can carry `aliases:` — a classified
// external hostname (api.stripe.com) is aliased to the proxy container on
// the SUT-side network, so Docker's embedded DNS resolves it to the proxy
// instead of failing to resolve at all (there is no real api.stripe.com
// container in this compose stack). Without this, "creating a Toxiproxy
// proxy" is inert: nothing tells the SUT to dial it instead of the real
// host. See ComposeTopologyApplier.Apply's doc comment for the fuller
// picture and its limits.
type overlayService struct {
	Image       string            `yaml:"image,omitempty"`
	Networks    any               `yaml:"networks"`
	Ports       []string          `yaml:"ports,omitempty"`
	Environment map[string]string `yaml:"environment,omitempty"`
	Command     []string          `yaml:"command,omitempty"`
	// Profiles disables a service under R-EXE-20's rename trick: a service
	// with a profile that is never activated is not started by `up`, which
	// is how the original internal dependency's own claim on its DNS name
	// is removed without needing compose to support an actual "rename".
	Profiles []string `yaml:"profiles,omitempty"`
	// DependsOn replaces (not merges with) this service's depends_on when
	// set; see overlayDependsOn's doc comment for why a plain []string here
	// would not be enough.
	DependsOn *overlayDependsOn `yaml:"depends_on,omitempty"`
}

// overlayDependsOn carries a service's rewritten depends_on list and
// marshals it under compose's own `!override` YAML tag rather than a plain
// sequence.
//
// R-EXE-20's rename trick disables an internal dependency's original
// service via an unused profile and clones it under backendServiceName.
// Any other service whose depends_on names the original (a directly
// observed real-repo pattern, per an E1 finding) must be repointed at the
// clone — but compose merges `-f` override files' depends_on additively by
// key (verified empirically: overriding a service to depend on only the
// clone still left the disabled original's key present in the merged
// result, because compose treats depends_on as a map to merge, not a list
// to replace), and a depends_on entry naming a profile-disabled service is
// a hard "depends on undefined service" error — the service is excluded
// from the project entirely, not just "not started". `!override` is
// compose's own escape hatch for exactly this: it replaces the attribute
// instead of merging it, so the disabled original's key is actually gone
// from the merged result, not just shadowed.
type overlayDependsOn struct {
	entries []string
}

func (d overlayDependsOn) MarshalYAML() (any, error) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!override"}
	for _, e := range d.entries {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: e})
	}
	return seq, nil
}

// tortureuDisabledProfile is never passed to `docker compose up`, so any
// service placed under it is simply never started (see overlayService's
// Profiles doc comment).
const tortureuDisabledProfile = "tortureu-disabled"

// stringEnv converts compose-go's MappingWithEquals (map[string]*string,
// nil meaning "inherit from the shell" — meaningless once cloned into a
// container definition with no such shell) into the plain map this
// package's overlayService carries, dropping any nil-valued entries.
func stringEnv(env types.MappingWithEquals) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

// backendServiceName is the name R-EXE-20's rename trick gives the real
// dependency once the proxy has taken over its original DNS name: SPEC.md's
// own worked example is exactly this shape (`db` -> `db-tortureu-backend`).
func backendServiceName(hostname string) string {
	return hostname + "-tortureu-backend"
}

// rewriteDependsOn reports the depends_on entries a service's overlay
// definition needs when at least one of its original dependencies names an
// internalHostnames entry (a host R-EXE-20 is disabling via profile and
// cloning under backendServiceName): each such name is repointed at its
// clone, everything else passes through unchanged. Returns ok=false (and a
// nil *overlayDependsOn, correctly omitted by overlayService's
// `omitempty`) when nothing needs rewriting, so unaffected services' compose
// files are left byte-for-byte as the base file already declares them —
// this only touches services actually affected by R-EXE-20's rename.
func rewriteDependsOn(deps types.DependsOnConfig, internalHostnames map[string]bool) (*overlayDependsOn, bool) {
	if len(deps) == 0 {
		return nil, false
	}
	names := make([]string, 0, len(deps))
	for name := range deps {
		names = append(names, name)
	}
	sort.Strings(names)

	changed := false
	entries := make([]string, 0, len(names))
	for _, name := range names {
		if internalHostnames[name] {
			entries = append(entries, backendServiceName(name))
			changed = true
			continue
		}
		entries = append(entries, name)
	}
	if !changed {
		return nil, false
	}
	return &overlayDependsOn{entries: entries}, true
}

// networkFaultVerbs mirrors R-EXE-15's Toxiproxy row: the verbs that need a
// network-level intercept (a Toxiproxy proxy) rather than a container-scoped
// Docker action. Duplicated from internal/fault (whose table is unexported)
// for the same reason internal/fault itself duplicates internal/config's —
// this package must decide, independently, which faults R-EXE-20's
// interception requirement applies to.
var networkFaultVerbs = map[string]bool{
	"latency":    true,
	"down":       true,
	"bandwidth":  true,
	"slicer":     true,
	"timeout":    true,
	"reset_peer": true,
}

type overlay struct {
	Networks map[string]overlayNetwork `yaml:"networks"`
	Services map[string]overlayService `yaml:"services"`
}

// overlayNetworkAttachment is the long form of a service's entry under one
// network key, used only for the proxy (see overlayService's doc comment).
type overlayNetworkAttachment struct {
	Aliases []string `yaml:"aliases,omitempty"`
}

// hostnameOf strips the port off a "host:port" egress key (config.Egress's
// map key shape, e.g. "api.stripe.com:443") for use as a DNS alias — a
// compose network alias is a bare hostname, never host:port.
func hostnameOf(hostPort string) string {
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		return h
	}
	return hostPort
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
	// ProxyControlPort overrides the package-level ProxyControlPort default
	// for this Apply call. Exists so two stacks — two test runs, two `go
	// test` invocations, or a stack this package's own test suite leaked
	// and never cleaned up — do not contend for the same fixed host port;
	// callers that generate a per-run identifier (this package's Docker
	// tests derive one from their own random suffix) can derive a per-run
	// port from it the same way. Empty means "use ProxyControlPort".
	ProxyControlPort string
	// OverlayPath overrides the default fixed location Apply writes the
	// merged compose overlay to (defaultOverlayPath). Two concurrent Apply
	// calls sharing that fixed path would race: the second call's write
	// clobbers the file the first call's own later `docker compose down`
	// still needs to identify the right resources to remove — a real
	// correctness bug for concurrent use, not just a test artifact,
	// surfaced by TestDC2Enforcement_TwoStacksCoexistConcurrently. Empty
	// means "use defaultOverlayPath", preserving every existing caller
	// (including this package's own tests written before this field
	// existed) that reads the overlay back from that fixed path after
	// Apply returns.
	OverlayPath string
}

// defaultOverlayPath is where Apply writes the merged compose overlay when
// OverlayPath is empty — unchanged from this package's original behavior,
// since several tests read the overlay back from this exact path after
// Apply returns.
func defaultOverlayPath() string {
	return filepath.Join(os.TempDir(), "tortureu-topology-overlay.yaml")
}

func (a ComposeTopologyApplier) overlayPath() string {
	if a.OverlayPath != "" {
		return a.OverlayPath
	}
	return defaultOverlayPath()
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

func (a ComposeTopologyApplier) controlPort() string {
	if a.ProxyControlPort != "" {
		return a.ProxyControlPort
	}
	return ProxyControlPort
}

// Apply enumerates composePath's services, writes an override file attaching
// every service except the proxy to top's internal network and the proxy
// container to both networks, then runs `docker compose -f composePath -f
// overlay <Up...>` (R-DC2-3, R-EXE-3: this must complete before the load
// generator's first request).
//
// externalHosts are the "host:port" egress keys classified mock or real.
// Each becomes a network alias on the proxy service (see overlayService's
// doc comment), so the SUT resolves that hostname to the proxy instead of
// nothing. class: block gets no alias at all — its hostname simply fails to
// resolve, a reasonable reading of "dropped silently".
//
// internalHosts are the "host:port" fault targets classified internal with
// a network-verb fault attached (R-EXE-20): existing compose dependencies
// (postgres, redis, ...) that already have their own container DNS
// identity, which the external-host aliasing above cannot reuse — aliasing
// the proxy to "redis" while the real redis container is *also* attached
// under that same name is an ambiguous double claim, and Docker's embedded
// DNS resolution between two identically-aliased containers is not
// something this package can rely on picking correctly (or at all).
//
// R-EXE-20's workable shape instead: for each internal host, the real
// dependency's service is moved aside — disabled under a profile that is
// never activated so it stops claiming that name at all — and a clone of
// its definition (image, environment, command) is declared under
// backendServiceName, attached only to the SUT network. The proxy then
// takes the *original* name as its own alias. The SUT's own configuration
// is untouched: it still dials "redis"; that name now resolves to the
// proxy, which forwards to the real container under its new name.
//
// If an internal host does not match any service compose-go actually
// parsed from composePath, Apply returns an error rather than silently
// proceeding — R-EXE-20's "run MUST fail loudly" applied at the one point
// in Run's flow (before load ever starts) that can still stop the run
// before a fault silently never reaches its target.
func (a ComposeTopologyApplier) Apply(composePath string, top egress.Topology, externalHosts, internalHosts []string) error {
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

	// R-EXE-20: every internal host must resolve to a service compose-go
	// actually parsed, or Apply fails loudly here — before load starts —
	// rather than letting a fault silently target nothing.
	internalHostnames := make(map[string]bool, len(internalHosts))
	for _, h := range internalHosts {
		hostname := hostnameOf(h)
		if _, ok := project.Services[hostname]; !ok {
			return fmt.Errorf("run: topology: internal-class fault target %q: no matching compose service %q found — cannot intercept it (R-EXE-20)", h, hostname)
		}
		internalHostnames[hostname] = true
	}

	var aliases []string
	for _, h := range externalHosts {
		aliases = append(aliases, hostnameOf(h))
	}
	for hostname := range internalHostnames {
		aliases = append(aliases, hostname)
	}
	sort.Strings(aliases)

	proxyNetworkMap := map[string]overlayNetworkAttachment{}
	for _, n := range proxyNetworks {
		if n == sutNetwork {
			proxyNetworkMap[n] = overlayNetworkAttachment{Aliases: aliases}
		} else {
			proxyNetworkMap[n] = overlayNetworkAttachment{}
		}
	}

	ov := overlay{
		Networks: map[string]overlayNetwork{},
		Services: map[string]overlayService{
			proxyName: {
				Image:    proxyImage,
				Networks: proxyNetworkMap,
				Ports:    []string{a.controlPort() + ":8474"},
			},
		},
	}
	for name, n := range top.Networks {
		ov.Networks[name] = overlayNetwork{Name: name, Internal: n.Internal}
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
		if internalHostnames[name] {
			// Disable the original: it must stop claiming this DNS name so
			// the proxy's alias (added above) is the only thing answering
			// for it (R-EXE-20). Its definition lives on under
			// backendServiceName instead, below.
			ov.Services[name] = overlayService{Networks: []string{sutNetwork}, Profiles: []string{tortureuDisabledProfile}}
			continue
		}
		plain := overlayService{Networks: []string{sutNetwork}}
		if dep, ok := rewriteDependsOn(project.Services[name].DependsOn, internalHostnames); ok {
			plain.DependsOn = dep
		}
		ov.Services[name] = plain
	}
	for hostname := range internalHostnames {
		svc := project.Services[hostname]
		backend := overlayService{
			Image:       svc.Image,
			Command:     []string(svc.Command),
			Environment: stringEnv(svc.Environment),
			Networks:    []string{sutNetwork},
		}
		if dep, ok := rewriteDependsOn(svc.DependsOn, internalHostnames); ok {
			backend.DependsOn = dep
		}
		ov.Services[backendServiceName(hostname)] = backend
	}

	out, err := yaml.Marshal(ov)
	if err != nil {
		return err
	}
	if err := os.WriteFile(a.overlayPath(), out, 0o644); err != nil {
		return fmt.Errorf("run: topology: write overlay: %w", err)
	}

	args := append([]string{"compose", "-f", absPath, "-f", a.overlayPath()}, a.up()...)
	cmd := exec.Command(a.bin(), args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("run: topology: %s %v: %w: %s", a.bin(), args, err, out)
	}
	return nil
}
