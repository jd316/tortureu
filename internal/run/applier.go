package run

import "github.com/jdb316/tortureu/internal/fault"

// CombinedApplier implements fault.Applier by routing to the backend that
// actually owns the action kind (R-EXE-15): network toxics to Toxiproxy,
// container/cgroup actions to Docker. fault.Manager.Apply always calls
// exactly one of ApplyToxic/ApplyDocker per action (its Kind decides which),
// so each method here only ever forwards to the one backend that can
// possibly be asked.
type CombinedApplier struct {
	Docker    fault.Applier
	Toxiproxy fault.Applier
}

func (c CombinedApplier) ApplyToxic(name string, t fault.Toxic) (func() error, error) {
	return c.Toxiproxy.ApplyToxic(name, t)
}

func (c CombinedApplier) ApplyDocker(name string, d fault.DockerAction) (func() error, error) {
	return c.Docker.ApplyDocker(name, d)
}

// RegisterTarget forwards to c.Toxiproxy if it supports registration (see
// ToxiproxyApplier's doc comment on why this indirection exists).
func (c CombinedApplier) RegisterTarget(faultName, target string) {
	registerToxicTarget(c.Toxiproxy, faultName, target)
}

// EnsureProxies forwards to c.Toxiproxy if it supports eager proxy creation
// (ToxiproxyApplier does; see its EnsureProxies doc comment on why Run calls
// this before load starts, not only lazily per fault).
func (c CombinedApplier) EnsureProxies(targets []string) error {
	if p, ok := c.Toxiproxy.(interface{ EnsureProxies([]string) error }); ok {
		return p.EnsureProxies(targets)
	}
	return nil
}
