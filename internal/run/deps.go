package run

import (
	realapplier "github.com/jd316/tortureu/internal/applier"
	"github.com/jd316/tortureu/internal/queuefault"
)

// NewRealDeps wires the concrete Docker/Toxiproxy/k6/Prometheus
// implementations built in this package into one Deps for production use.
// toxiproxyURL and promURL are the control-plane addresses; pass "" for
// toxiproxyURL to default to "http://localhost:<ProxyControlPort>" (the
// fixed host port ComposeTopologyApplier's overlay publishes — see
// ProxyControlPort's doc comment), and "" for promURL when no Prometheus
// endpoint is configured (promql: asserts are then skipped, not silently
// passed — see evaluatePromqlAsserts).
//
// This is the original two-argument constructor, kept so existing callers
// (cmd/tortureu/run.go) keep compiling unchanged; it leaves QueueApplier
// and MockApplier nil, same as before internal/applier existed. Use
// NewRealDepsFull to also wire error_rate/poison_pill/duplicate against a
// real WireMock/broker endpoint — cmd/tortureu adopting it (new
// --mock-url/--broker-url flags) is a follow-up outside this task's
// touch-only-internal/run scope, flagged in the Task 7 report.
func NewRealDeps(toxiproxyURL, promURL string) Deps {
	return NewRealDepsFull(toxiproxyURL, promURL, "", "", nil)
}

// NewRealDepsFull is NewRealDeps plus internal/applier's real owners for
// R-EXE-19's remaining two rows (Task 10): mockURL wires WireMockApplier
// for error_rate against a class: mock host, and brokerURL wires
// BrokerApplier for poison_pill/duplicate. Neither address is hardcoded —
// both are read from wherever the caller resolved them (e.g. CLI flags or
// torture.yaml), same as toxiproxyURL/promURL. "" leaves the corresponding
// Deps field nil: a run that then declares the verb fails loudly (R-EXE-19)
// rather than silently skipping it.
//
// v0 limitation, not fixed here: MockApplier is a single WireMockApplier,
// but internal/applier's own doc comment notes "one WireMock instance per
// mocked host" is the natural shape — a torture.yaml with more than one
// class: mock host would need one address per host, not one shared BaseURL.
// Scoped out for this task; flagged for whoever wires multi-host mock
// support.
func NewRealDepsFull(toxiproxyURL, promURL, mockURL, brokerURL string, sql SQLQuerier) Deps {
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
	var mock MockApplier
	if mockURL != "" {
		mock = &realapplier.WireMockApplier{BaseURL: mockURL}
	}
	var queue queuefault.Applier
	if brokerURL != "" {
		queue = &realapplier.BrokerApplier{BaseURL: brokerURL}
	}
	// DBLoad/Fuzz are always wired: unlike the endpoints above they need no
	// address to construct, and they do nothing at all unless
	// Options.DBLoad / Options.Fuzz is set. Leaving them nil would make
	// `run --db-load` refuse on a machine where pgbench is installed and
	// everything else is in order — the "built but unwired" failure this
	// project has hit three times (R-EXE-26, R-EXE-27).
	return Deps{
		Reset:        ShellResetter{},
		DBLoad:       PgbenchRunner{},
		Fuzz:         SchemathesisRunner{},
		Topology:     ComposeTopologyApplier{},
		Load:         &K6Runner{},
		Applier:      applier,
		QueueApplier: queue,
		MockApplier:  mock,
		Prom:         prom,
		SQL:          sql,
	}
}
