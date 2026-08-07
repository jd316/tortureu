package egress

// Network is one docker-compose network entry. Internal:true gives it no
// default route off-box — Docker's own enforcement, no cooperation required
// from the application (R-DC2-3).
type Network struct {
	Internal bool `yaml:"internal,omitempty"`
}

// Service is the subset of a docker-compose service definition the egress
// topology needs to express: which networks it is attached to.
type Service struct {
	Networks []string `yaml:"networks"`
}

// Topology is a docker-compose overlay implementing R-DC2-3's topological
// guarantee. The orchestrator (internal/run) merges this over the user's
// compose file: the SUT ends up on an internal:true network with no route
// out, and the TortureU proxy is the only service dual-homed onto both that
// network and the outside-reaching egress network — so it is the SUT's only
// path off-box, enforced by the network itself rather than by policy.
type Topology struct {
	Networks map[string]Network `yaml:"networks"`
	Services map[string]Service `yaml:"services"`
}

// BuildTopology returns the R-DC2-3 overlay for a compose stack where sutNetwork
// carries the system under test, egressNetwork is a normal bridge network
// reaching the outside world, and proxyService is the TortureU proxy
// dual-homed on both.
func BuildTopology(sutNetwork, egressNetwork, proxyService string) Topology {
	return Topology{
		Networks: map[string]Network{
			sutNetwork:    {Internal: true},
			egressNetwork: {Internal: false},
		},
		Services: map[string]Service{
			proxyService: {Networks: []string{sutNetwork, egressNetwork}},
		},
	}
}
