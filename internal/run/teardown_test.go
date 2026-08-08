package run

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
)

// blockingLoadHandle never delivers anything on Done()/Err() until the test
// is finished with it — it stands in for a load run that is still in
// progress when a signal interrupts Run.
type blockingLoadHandle struct {
	markers chan PhaseMarker
	done    chan LoadResult
	errCh   chan error
}

func newBlockingLoadHandle() *blockingLoadHandle {
	return &blockingLoadHandle{
		markers: make(chan PhaseMarker, 8),
		done:    make(chan LoadResult),
		errCh:   make(chan error),
	}
}

func (h *blockingLoadHandle) Markers() <-chan PhaseMarker { return h.markers }
func (h *blockingLoadHandle) Done() <-chan LoadResult     { return h.done }
func (h *blockingLoadHandle) Err() <-chan error           { return h.errCh }

// spec: R-EXE-16
func TestRun_SignalMidLoadTearsDownForDurationFaultsToo(t *testing.T) {
	// Reproduces the exact window the review flagged: a fault applied with
	// a `for:` duration is tracked outside fault.Manager (see scheduler.go),
	// so only teardownExpiring — not fault.Manager.Teardown alone — removes
	// it. A signal arriving mid-load, while Run is blocked waiting on
	// handle.Done(), must still reach it.
	cfg := minimalConfig()
	cfg.Faults = []config.Fault{{
		Name: "f1", At: "t=0s", For: "10s",
		Target: "checkout-api", Verb: "pause", Inject: map[string]any{"pause": true},
	}}
	sys := detect.System{}

	handle := newBlockingLoadHandle()
	applier := &trackingApplier{}

	done := make(chan struct{})
	var gotStatus string
	go func() {
		v := Run(cfg, sys, Deps{
			Reset:    &fakeResetter{},
			Topology: &fakeTopology{},
			Load:     &fakeLoadRunner{handle: handle},
			Applier:  applier,
		}, Options{})
		gotStatus = string(v.Status)
		close(done)
	}()

	// Give the scheduler time to apply the t=0s, for:10s fault before the
	// signal arrives — otherwise there is nothing yet to prove teardown of.
	deadline := time.Now().Add(2 * time.Second)
	for applier.applyCalls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("fault was never applied within 2s")
		}
		time.Sleep(time.Millisecond)
	}

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT to self: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s of SIGINT")
	}

	if gotStatus != "aborted" {
		t.Errorf("Status = %q, want aborted", gotStatus)
	}
	if applier.undoCalls.Load() == 0 {
		t.Error("fault was never torn down after SIGINT — R-EXE-16 requires teardown regardless of when the signal arrives, including a for:-duration fault tracked outside fault.Manager")
	}
}

// panickingQuerier panics on Query, standing in for a misbehaving
// PromQuerier dependency. It runs synchronously inside Run's own call stack
// (Run -> evaluatePromqlAsserts -> Query), unlike a fault applied in its own
// goroutine — the safe, deterministic way to prove Run's own top-level
// recover reaches teardownExpiring, without relying on an actual OS signal
// or a panic escaping an unrelated goroutine (which Go cannot recover from
// at all, anywhere).
type panickingQuerier struct{}

func (panickingQuerier) Query(expr string) (bool, string, error) {
	panic(errors.New("simulated PromQuerier failure"))
}

// spec: R-EXE-5
func TestRun_PanicMidRunTearsDownForDurationFaultsToo(t *testing.T) {
	cfg := minimalConfig()
	cfg.Faults = []config.Fault{{
		Name: "f1", At: "t=0s", For: "10s",
		Target: "checkout-api", Verb: "pause", Inject: map[string]any{"pause": true},
	}}
	cfg.Assert = append(cfg.Assert, config.AssertEntry{"promql": "up == 1"})

	handle := newFakeLoadHandle()
	applier := &trackingApplier{}

	go func() {
		time.Sleep(30 * time.Millisecond) // let the t=0s fault apply first
		handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}
	}()

	v := Run(cfg, detect.System{}, Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: handle},
		Applier:  applier,
		Prom:     panickingQuerier{},
	}, Options{})

	if v.Status != "error" {
		t.Errorf("Status = %q, want error", v.Status)
	}
	if applier.applyCalls.Load() == 0 {
		t.Fatal("fault was never applied — test setup is broken")
	}
	if applier.undoCalls.Load() == 0 {
		t.Error("fault was never torn down after a mid-run panic — R-EXE-5 requires teardown on panic, including a for:-duration fault tracked outside fault.Manager")
	}
	// spec: R-VER-2
	if v.Error == "" {
		t.Error("Error is empty on a status:error verdict — R-VER-2 requires a reason distinguishing \"the tool broke\" from \"the SUT broke\", and an error with no reason is indistinguishable from a shrug")
	}
}
