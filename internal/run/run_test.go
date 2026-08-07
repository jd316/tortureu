package run

import (
	"errors"
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

func (f *fakeTopology) Apply(composePath string, top egress.Topology) error {
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
	handle *fakeLoadHandle
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

func (o *orderRecordingTopology) Apply(composePath string, top egress.Topology) error {
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

	if applier.applyCalls == 0 {
		t.Fatal("fault was never applied — test setup is broken")
	}
	if applier.undoCalls == 0 {
		t.Error("fault was never torn down — R-EXE-5 requires every applied fault to be removed before the run ends")
	}
}

type trackingApplier struct {
	applyCalls int
	undoCalls  int
}

func (a *trackingApplier) ApplyToxic(name string, t fault.Toxic) (func() error, error) {
	return nil, errors.New("not used in this test")
}

func (a *trackingApplier) ApplyDocker(name string, d fault.DockerAction) (func() error, error) {
	a.applyCalls++
	return func() error {
		a.undoCalls++
		return nil
	}, nil
}
