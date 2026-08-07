package run

import (
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/fault"
	"github.com/jdb316/tortureu/internal/queuefault"
)

// spec: R-EXE-19
func TestScheduleFaults_RoutesPassedOverVerbToItsOwner(t *testing.T) {
	f := config.Fault{
		Name:   "dup1",
		At:     "t=0s",
		Target: "orders-topic",
		Verb:   "duplicate",
		Inject: map[string]any{"duplicate": 0.1},
	}
	qa := &fakeQueueApplier{}
	markers := make(chan PhaseMarker)
	close(markers)

	done, _ := scheduleFaultsWithQueue([]config.Fault{f}, markers, time.Now(), &fault.Manager{}, &fakeApplier{}, qa)
	errs := <-done

	if qa.duplicateCalls != 1 {
		t.Fatalf("ApplyDuplicate calls = %d, want 1 — a passed-over verb must reach its owning layer (R-EXE-19), not vanish", qa.duplicateCalls)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none: routing to a wired owner must not fail the run", errs)
	}
}

// spec: R-EXE-19
func TestScheduleFaults_FailsRunWhenPassedOverVerbHasNoWiredOwner(t *testing.T) {
	f := config.Fault{
		Name:   "dup1",
		At:     "t=0s",
		Target: "orders-topic",
		Verb:   "duplicate",
		Inject: map[string]any{"duplicate": 0.1},
	}
	markers := make(chan PhaseMarker)
	close(markers)

	// No queue applier wired (nil) — R-EXE-19: the run must fail rather
	// than silently proceed as if the fault had been applied.
	done, _ := scheduleFaultsWithQueue([]config.Fault{f}, markers, time.Now(), &fault.Manager{}, &fakeApplier{}, nil)
	errs := <-done

	if len(errs) == 0 {
		t.Fatal("errs is empty, want an error naming the unroutable verb — a run that cannot apply a declared fault must not proceed silently (R-EXE-19)")
	}
}

// spec: R-EXE-19
func TestScheduleFaults_ErrorRateFailsRunNoMockApplyCapabilityWired(t *testing.T) {
	// error_rate is only legal on a class: mock host, validated by
	// internal/egress.ValidateErrorRate (R-CFG-23) — but no package applies
	// it against a live WireMock instance, so even a legal value must fail
	// the run rather than report a pass that never injected anything.
	f := config.Fault{
		Name:   "stripe_errors",
		At:     "t=0s",
		Target: "api.stripe.com",
		Verb:   "error_rate",
		Inject: map[string]any{"error_rate": 0.15},
	}
	markers := make(chan PhaseMarker)
	close(markers)

	done, _ := scheduleFaultsWithQueue([]config.Fault{f}, markers, time.Now(), &fault.Manager{}, &fakeApplier{}, nil)
	errs := <-done

	if len(errs) == 0 {
		t.Fatal("errs is empty, want an error: error_rate has no application mechanism wired, so the run must fail rather than silently pass (R-EXE-19)")
	}
}

type fakeQueueApplier struct {
	poisonPillCalls int
	duplicateCalls  int
}

func (f *fakeQueueApplier) ApplyPoisonPill(name string, p queuefault.PoisonPill) (func() error, error) {
	f.poisonPillCalls++
	return func() error { return nil }, nil
}

func (f *fakeQueueApplier) ApplyDuplicate(name string, d queuefault.Duplicate) (func() error, error) {
	f.duplicateCalls++
	return func() error { return nil }, nil
}
