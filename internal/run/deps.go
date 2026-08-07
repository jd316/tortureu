package run

// NewRealDeps wires the concrete Docker/Toxiproxy/k6/Prometheus
// implementations built in this package into one Deps for production use.
// toxiproxyURL and promURL are the control-plane addresses; pass "" for
// toxiproxyURL to default to "http://localhost:<ProxyControlPort>" (the
// fixed host port ComposeTopologyApplier's overlay publishes — see
// ProxyControlPort's doc comment), and "" for promURL when no Prometheus
// endpoint is configured (promql: asserts are then skipped, not silently
// passed — see evaluatePromqlAsserts).
//
// QueueApplier is left nil: no broker client (Kafka or otherwise) is a
// go.mod dependency of this project, and building one is outside this
// task's touch-only-internal/run scope. Per R-EXE-19, any run declaring
// poison_pill or duplicate against this Deps value fails loudly rather than
// silently skipping the fault — escalated in the Task 7 report as needing a
// broker-client decision from whoever owns that choice.
func NewRealDeps(toxiproxyURL, promURL string) Deps {
	if toxiproxyURL == "" {
		toxiproxyURL = "http://localhost:" + ProxyControlPort
	}
	applier := CombinedApplier{
		Docker:    DockerApplier{},
		Toxiproxy: &ToxiproxyApplier{BaseURL: toxiproxyURL},
	}
	var prom PromQuerier
	if promURL != "" {
		prom = HTTPPromQuerier{BaseURL: promURL}
	}
	return Deps{
		Reset:    ShellResetter{},
		Topology: ComposeTopologyApplier{},
		Load:     K6Runner{},
		Applier:  applier,
		Prom:     prom,
	}
}
