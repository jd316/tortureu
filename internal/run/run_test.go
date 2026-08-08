package run

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/egress"
	"github.com/jdb316/tortureu/internal/fault"
	"github.com/jdb316/tortureu/internal/verdict"
)

// fakeResetter records whether Reset was called and returns a canned error.
type fakeResetter struct {
	called bool
	err    error
}

func (f *fakeResetter) Reset(command string) error {
	f.called = true
	return f.err
}

// fakeTopology records whether Apply was called and returns a canned error.
type fakeTopology struct {
	called bool
	err    error
}

func (f *fakeTopology) Apply(composePath string, top egress.Topology, externalHosts, internalHosts []string) error {
	f.called = true
	return f.err
}

// fakeLoadHandle is a no-op LoadHandle: nothing on any channel unless the
// test sends it.
type fakeLoadHandle struct {
	markers chan PhaseMarker
	done    chan LoadResult
	errCh   chan error
}

func newFakeLoadHandle() *fakeLoadHandle {
	return &fakeLoadHandle{
		markers: make(chan PhaseMarker, 8),
		done:    make(chan LoadResult, 1),
		errCh:   make(chan error, 1),
	}
}

func (h *fakeLoadHandle) Markers() <-chan PhaseMarker { return h.markers }
func (h *fakeLoadHandle) Done() <-chan LoadResult     { return h.done }
func (h *fakeLoadHandle) Err() <-chan error           { return h.errCh }

// fakeLoadRunner records whether Start was called.
type fakeLoadRunner struct {
	called bool
	handle LoadHandle
	err    error
}

func (f *fakeLoadRunner) Start(script string) (LoadHandle, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	return f.handle, nil
}

// fakeApplier never gets a real target; tests that reach it want to know
// whether it was ever invoked.
type fakeApplier struct {
	toxicCalls  int
	dockerCalls int
}

func (f *fakeApplier) ApplyToxic(name string, t fault.Toxic) (func() error, error) {
	f.toxicCalls++
	return func() error { return nil }, nil
}

func (f *fakeApplier) ApplyDocker(name string, d fault.DockerAction) (func() error, error) {
	f.dockerCalls++
	return func() error { return nil }, nil
}

func minimalConfig() *config.Config {
	return &config.Config{
		Target: config.Target{Compose: "docker-compose.yml", Service: "checkout-api", BaseURL: "http://localhost:8080"},
		Egress: config.Egress{Default: "deny", Hosts: map[string]config.EgressHost{}},
		Load: config.Load{
			Model:  "arrival_rate",
			Stages: []config.Stage{{Phase: "peak", Hold: "10rps", For: "1s"}},
		},
		Assert: []config.AssertEntry{{"http_req_duration": []string{"p(95)<500"}}},
		Reset:  config.Reset{Command: "true"},
	}
}

// spec: R-VER-8
func TestRun_AllUnevaluatedAssertsNeverExitZero(t *testing.T) {
	// A torture.yaml whose assert: block is entirely promql: (or sql:, see
	// findings_test.go's TestEvaluateSQLAsserts_AlwaysUnevaluated), with no
	// -prom-url configured, must not run green: nothing was actually
	// checked. R-VER-8's exit 4 exists precisely for "we could not tell" —
	// a run like this must land there, never at exit 0.
	cfg := minimalConfig()
	cfg.Assert = []config.AssertEntry{{"promql": "orders_total == payments_total"}}
	sys := detect.System{}

	handle := newFakeLoadHandle()
	handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}

	v := Run(cfg, sys, Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: handle},
		Applier:  &fakeApplier{},
		Prom:     nil, // no -prom-url configured
	}, Options{})

	if verdict.ExitCode(*v) == 0 {
		t.Fatalf("ExitCode = 0, want non-zero — every assertion in this run was unevaluated, not passing (status=%q, findings=%v)", v.Status, v.Findings)
	}
	if verdict.ExitCode(*v) != 4 {
		t.Errorf("ExitCode = %d, want 4 (inconclusive) — the trigger is status:fail with every finding ambiguous, which an all-unevaluated run satisfies", verdict.ExitCode(*v))
	}
	if len(v.Findings) == 0 {
		t.Error("Findings is empty, want the unevaluated promql assertion recorded as a finding, not silently dropped")
	}
}

// spec: R-VER-3
func TestRun_FindingIDsAreUniqueAcrossMergedSources(t *testing.T) {
	// evaluateThresholds and evaluatePromqlAsserts each used to number
	// their own findings from f1, so a run with a broken k6 threshold AND
	// an unevaluated promql assertion could emit two findings sharing ID
	// "f1" — the second unaddressable by an agent calling explain_failure
	// (it returns the first match).
	cfg := minimalConfig()
	cfg.Assert = []config.AssertEntry{
		{"http_req_duration": []string{"p(95)<500"}},
		{"promql": "orders_total == payments_total"},
	}
	sys := detect.System{}

	handle := newFakeLoadHandle()
	handle.done <- LoadResult{SummaryJSON: []byte(`{
		"metrics": {
			"http_req_duration": {"thresholds": {"p(95)<500": {"ok": false}}}
		}
	}`)}

	v := Run(cfg, sys, Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: handle},
		Applier:  &fakeApplier{},
		Prom:     nil,
	}, Options{})

	if len(v.Findings) != 2 {
		t.Fatalf("Findings = %v, want exactly two (one broken threshold, one unevaluated promql assert)", v.Findings)
	}
	seen := map[string]bool{}
	for _, f := range v.Findings {
		if f.ID == "" {
			t.Errorf("finding %+v has no ID", f)
		}
		if seen[f.ID] {
			t.Errorf("finding ID %q used more than once — an agent calling explain_failure could not address the second one", f.ID)
		}
		seen[f.ID] = true
	}
}

// fakeContainerAwareLoadRunner is a fakeLoadRunner that also implements
// SetSUTContainer, so Run's optional wiring (the interface{ SetSUTContainer
// } duck type) actually engages for it.
type fakeContainerAwareLoadRunner struct {
	fakeLoadRunner
	sutContainer string
}

func (f *fakeContainerAwareLoadRunner) SetSUTContainer(name string) {
	f.sutContainer = name
}

// withFakeSUTDiscovery overrides discoverSUTContainer for the duration of a
// test and restores it afterward — this package's own docker-backed tests
// prove the real implementation; this lets Run's wiring around it be
// proven without a live daemon.
func withFakeSUTDiscovery(t *testing.T, fn func(service string) (string, error)) {
	t.Helper()
	orig := discoverSUTContainer
	discoverSUTContainer = fn
	t.Cleanup(func() { discoverSUTContainer = orig })
}

// spec: R-DC2-3
func TestRun_WiresSUTContainerIntoLoadRunnerAfterTopologyApply(t *testing.T) {
	// R-DC2-3's own network makes the SUT's published ports unreachable
	// from the host (the eval-found bug this fixes): a LoadRunner that
	// knows how to run k6 sharing the SUT's network namespace instead
	// needs to be told which container that is. Run must discover it
	// after Topology.Apply and before Load.Start.
	cfg := minimalConfig()
	sys := detect.System{}
	withFakeSUTDiscovery(t, func(service string) (string, error) {
		if service != cfg.Target.Service {
			t.Errorf("discoverSUTContainer called with %q, want %q", service, cfg.Target.Service)
		}
		return "checkout-api-container-1", nil
	})

	handle := newFakeLoadHandle()
	handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}
	load := &fakeContainerAwareLoadRunner{fakeLoadRunner: fakeLoadRunner{handle: handle}}

	Run(cfg, sys, Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     load,
		Applier:  &fakeApplier{},
	}, Options{})

	if load.sutContainer != "checkout-api-container-1" {
		t.Errorf("SetSUTContainer received %q, want the discovered container name", load.sutContainer)
	}
}

// spec: R-VER-2
func TestRun_FailsLoudlyWhenSUTContainerCannotBeDiscovered(t *testing.T) {
	// A LoadRunner that needs the SUT's container to run k6 against it at
	// all (R-DC2-3's fix) must not silently fall back to the host-process
	// path this task's eval proved is always connection-refused against an
	// internal-only SUT — that would quietly reintroduce the exact bug.
	cfg := minimalConfig()
	sys := detect.System{}
	withFakeSUTDiscovery(t, func(service string) (string, error) {
		return "", errors.New("no running container found")
	})

	load := &fakeContainerAwareLoadRunner{fakeLoadRunner: fakeLoadRunner{handle: newFakeLoadHandle()}}
	v := Run(cfg, sys, Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     load,
		Applier:  &fakeApplier{},
	}, Options{})

	if v.Status != verdict.StatusError {
		t.Fatalf("Status = %q, want error", v.Status)
	}
	if load.called {
		t.Error("Load.Start was called despite SUT container discovery failing")
	}
	if !strings.Contains(v.Error, "SUT container") {
		t.Errorf("Error = %q, want it to name the actual cause", v.Error)
	}
}

// spec: R-VER-2
func TestRun_TopologyApplyFailureSetsErrorReason(t *testing.T) {
	// R-VER-2: status:error means the tool itself broke, distinct from
	// status:fail (the SUT broke an assertion) — a user cannot tell which
	// they're looking at from status alone. The underlying cause (e.g. "no
	// Dockerfile for this service") must actually reach the verdict, not
	// be discarded the moment Run decides to fail.
	cfg := minimalConfig()
	sys := detect.System{}
	wantErr := errors.New("docker compose build: no Dockerfile for service checkout-api")

	v := Run(cfg, sys, Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{err: wantErr},
		Load:     &fakeLoadRunner{handle: newFakeLoadHandle()},
		Applier:  &fakeApplier{},
	}, Options{})

	if v.Status != verdict.StatusError {
		t.Fatalf("Status = %q, want error", v.Status)
	}
	if v.Error == "" {
		t.Fatal("Error is empty on a status:error verdict")
	}
	if !strings.Contains(v.Error, wantErr.Error()) {
		t.Errorf("Error = %q, want it to contain the underlying cause %q (wrap, don't replace)", v.Error, wantErr.Error())
	}
}

// spec: R-VER-2
func TestRun_LoadStartFailureSetsErrorReason(t *testing.T) {
	// The load generator failing to start — most commonly because k6 isn't
	// installed (R-LIC-1: this tool never bundles it) — must not read as a
	// blank ERROR: this is the most likely first-run outcome for anyone
	// trying the tool for the first time.
	cfg := minimalConfig()
	sys := detect.System{}
	wantErr := errors.New("k6 not found on PATH (looked for \"k6\") — install k6 (https://k6.io) or pass -k6-path")

	v := Run(cfg, sys, Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{err: wantErr},
		Applier:  &fakeApplier{},
	}, Options{})

	if v.Status != verdict.StatusError {
		t.Fatalf("Status = %q, want error", v.Status)
	}
	if !strings.Contains(v.Error, "k6 not found") {
		t.Errorf("Error = %q, want it to name the actual cause (k6 missing), not a generic failure message", v.Error)
	}
}

// spec: R-EXE-20
func TestRun_FailsLoudlyWhenInternalFaultTargetCannotBeIntercepted(t *testing.T) {
	// A fault targeting a class: internal dependency with a network verb
	// (pg_slow/redis_dies-shaped — R-EXE-20's primary case) requires
	// Topology.Apply to perform the rename+alias interception. If Apply
	// can't do that (ComposeTopologyApplier.Apply returns an error when the
	// target names no real compose service — see topology_test.go's
	// TestComposeTopologyApplier_FailsLoudlyWhenInternalHostIsNotAKnownService),
	// Run must fail the whole run rather than proceed into a load where the
	// fault silently never reaches its target.
	cfg := minimalConfig()
	cfg.Egress.Hosts["postgres:5432"] = config.EgressHost{Class: "internal"}
	cfg.Faults = []config.Fault{{
		Name: "pg_slow", At: "t=0s", Target: "postgres:5432",
		Verb: "latency", Inject: map[string]any{"latency": "300ms"},
	}}
	sys := detect.System{EgressClass: map[string]string{"postgres:5432": "internal"}}

	topo := &fakeTopology{err: errors.New("postgres:5432: no matching compose service (R-EXE-20)")}
	load := &fakeLoadRunner{handle: newFakeLoadHandle()}

	v := Run(cfg, sys, Deps{
		Reset:    &fakeResetter{},
		Topology: topo,
		Load:     load,
		Applier:  &fakeApplier{},
	}, Options{})

	if v.Status != verdict.StatusError {
		t.Fatalf("Status = %q, want error", v.Status)
	}
	if load.called {
		t.Error("Load.Start was called despite Topology.Apply failing — R-EXE-20 requires failing before load starts, not after a fault silently never fires")
	}
}

// spec: R-DC2-2
func TestRun_AbortsBeforeLoadOnUnclassifiedEgress(t *testing.T) {
	cfg := minimalConfig()
	sys := detect.System{EgressClass: map[string]string{"unknown-host:443": "unclassified"}}

	reset := &fakeResetter{}
	topo := &fakeTopology{}
	load := &fakeLoadRunner{handle: newFakeLoadHandle()}

	v := Run(cfg, sys, Deps{Reset: reset, Topology: topo, Load: load, Applier: &fakeApplier{}}, Options{})

	if v.Status != verdict.StatusAborted {
		t.Fatalf("Status = %q, want aborted", v.Status)
	}
	if verdict.ExitCode(*v) != 3 {
		t.Errorf("ExitCode = %d, want 3", verdict.ExitCode(*v))
	}
	if load.called {
		t.Error("Load.Start was called despite an unclassified host — R-DC2-2 requires abort before any load starts")
	}
	if topo.called {
		t.Error("Topology.Apply was called despite an unclassified host — enforcement setup must not proceed past an abort either")
	}
	if len(v.EgressAudit.Unclassified) != 1 || v.EgressAudit.Unclassified[0] != "unknown-host:443" {
		t.Errorf("EgressAudit.Unclassified = %v, want the offending host named (R-DC2-2)", v.EgressAudit.Unclassified)
	}
}

// spec: R-EXE-2
func TestRun_ResetRunsBeforeClassificationAndLoad(t *testing.T) {
	cfg := minimalConfig()
	sys := detect.System{}

	reset := &fakeResetter{err: errors.New("reset failed")}
	topo := &fakeTopology{}
	load := &fakeLoadRunner{handle: newFakeLoadHandle()}

	v := Run(cfg, sys, Deps{Reset: reset, Topology: topo, Load: load, Applier: &fakeApplier{}}, Options{})

	if !reset.called {
		t.Fatal("Reset was never called")
	}
	if v.Status != verdict.StatusAborted {
		t.Fatalf("Status = %q, want aborted when reset fails", v.Status)
	}
	if load.called {
		t.Error("Load.Start was called despite reset failing — R-EXE-2 requires reset to complete before load begins")
	}
}

// spec: R-CFG-20
func TestRun_SkipsResetWithNoReset(t *testing.T) {
	cfg := minimalConfig()
	sys := detect.System{}

	reset := &fakeResetter{}
	topo := &fakeTopology{}
	handle := newFakeLoadHandle()
	handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}
	load := &fakeLoadRunner{handle: handle}

	v := Run(cfg, sys, Deps{Reset: reset, Topology: topo, Load: load, Applier: &fakeApplier{}}, Options{NoReset: true})

	if reset.called {
		t.Error("Reset was called despite --no-reset (R-CFG-20)")
	}
	if v.Reset != "skipped" {
		t.Errorf("Reset field = %q, want \"skipped\"", v.Reset)
	}
}

// spec: R-DC2-3
// spec: R-EXE-3
func TestRun_AppliesTopologyBeforeStartingLoad(t *testing.T) {
	cfg := minimalConfig()
	sys := detect.System{}

	reset := &fakeResetter{}
	var order []string
	topo := &orderRecordingTopology{order: &order}
	handle := newFakeLoadHandle()
	handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}
	load := &orderRecordingLoad{order: &order, handle: handle}

	Run(cfg, sys, Deps{Reset: reset, Topology: topo, Load: load, Applier: &fakeApplier{}}, Options{})

	if len(order) != 2 || order[0] != "topology" || order[1] != "load" {
		t.Fatalf("call order = %v, want [topology load] — R-DC2-3/R-EXE-3 require enforcement active before the first request", order)
	}
}

type orderRecordingTopology struct{ order *[]string }

func (o *orderRecordingTopology) Apply(composePath string, top egress.Topology, externalHosts, internalHosts []string) error {
	*o.order = append(*o.order, "topology")
	return nil
}

type orderRecordingLoad struct {
	order  *[]string
	handle LoadHandle
}

func (o *orderRecordingLoad) Start(script string) (LoadHandle, error) {
	*o.order = append(*o.order, "load")
	return o.handle, nil
}

// spec: R-EXE-5
func TestRun_TearsDownFaultsBeforeVerdictOnNormalCompletion(t *testing.T) {
	cfg := minimalConfig()
	cfg.Faults = []config.Fault{{Name: "f1", At: "t=0s", Target: "checkout-api", Verb: "pause", Inject: map[string]any{"pause": true}}}
	sys := detect.System{}

	handle := newFakeLoadHandle()
	applier := &trackingApplier{}

	go func() {
		// Give the scheduler a moment to apply the t=0s fault, then finish
		// the load so Run proceeds to its teardown step.
		time.Sleep(30 * time.Millisecond)
		handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}
	}()

	Run(cfg, sys, Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: handle},
		Applier:  applier,
	}, Options{})

	if applier.applyCalls.Load() == 0 {
		t.Fatal("fault was never applied — test setup is broken")
	}
	if applier.undoCalls.Load() == 0 {
		t.Error("fault was never torn down — R-EXE-5 requires every applied fault to be removed before the run ends")
	}
}

// trackingApplier's counters are plain ints read from the test goroutine
// while scheduleFaults's own goroutines write them concurrently (some tests
// poll applyCalls before the synchronizing schedDone channel receive that
// would otherwise happen-before a safe read) — atomic keeps that race-free
// without changing what the counts mean.
type trackingApplier struct {
	applyCalls atomic.Int64
	undoCalls  atomic.Int64
}

func (a *trackingApplier) ApplyToxic(name string, t fault.Toxic) (func() error, error) {
	return nil, errors.New("not used in this test")
}

func (a *trackingApplier) ApplyDocker(name string, d fault.DockerAction) (func() error, error) {
	a.applyCalls.Add(1)
	return func() error {
		a.undoCalls.Add(1)
		return nil
	}, nil
}
