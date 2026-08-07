package run

import (
	"testing"
	"time"

	realapplier "github.com/jdb316/tortureu/internal/applier"
	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/egress"
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

	done, _ := scheduleFaultsWithQueue([]config.Fault{f}, markers, time.Now(), &fault.Manager{}, &fakeApplier{}, qa, nil, nil)
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
	done, _ := scheduleFaultsWithQueue([]config.Fault{f}, markers, time.Now(), &fault.Manager{}, &fakeApplier{}, nil, nil, nil)
	errs := <-done

	if len(errs) == 0 {
		t.Fatal("errs is empty, want an error naming the unroutable verb — a run that cannot apply a declared fault must not proceed silently (R-EXE-19)")
	}
}

// spec: R-EXE-19
func TestScheduleFaults_ErrorRateFailsRunNoMockApplyCapabilityWired(t *testing.T) {
	// error_rate is only legal on a class: mock host, validated by
	// internal/egress.ValidateErrorRate (R-CFG-23) — but with no mock
	// applier wired, even a legal value against a genuinely class: mock
	// host must fail the run rather than report a pass that never actually
	// injected anything.
	f := config.Fault{
		Name:   "stripe_errors",
		At:     "t=0s",
		Target: "api.stripe.com",
		Verb:   "error_rate",
		Inject: map[string]any{"error_rate": 0.15},
	}
	markers := make(chan PhaseMarker)
	close(markers)
	classes := map[string]egress.Class{"api.stripe.com": egress.ClassMock}

	done, _ := scheduleFaultsWithQueue([]config.Fault{f}, markers, time.Now(), &fault.Manager{}, &fakeApplier{}, nil, nil, classes)
	errs := <-done

	if len(errs) == 0 {
		t.Fatal("errs is empty, want an error: error_rate has no mock applier wired, so the run must fail rather than silently pass (R-EXE-19)")
	}
}

// spec: R-EXE-19
func TestScheduleFaults_ErrorRateRoutesToMockApplierWhenTargetIsClassMock(t *testing.T) {
	// internal/applier.WireMockApplier (Task 10) is the real owner Task 7
	// never wired in: this proves error_rate now reaches it instead of
	// failing the run.
	f := config.Fault{
		Name:   "stripe_errors",
		At:     "t=0s",
		Target: "api.stripe.com",
		Verb:   "error_rate",
		Inject: map[string]any{"error_rate": 0.15},
	}
	markers := make(chan PhaseMarker)
	close(markers)
	classes := map[string]egress.Class{"api.stripe.com": egress.ClassMock}
	ma := &fakeMockApplier{}

	done, _ := scheduleFaultsWithQueue([]config.Fault{f}, markers, time.Now(), &fault.Manager{}, &fakeApplier{}, nil, ma, classes)
	errs := <-done

	if ma.calls != 1 {
		t.Fatalf("ApplyErrorRate calls = %d, want 1 — a passed-over verb with a wired owner must actually reach it (R-EXE-19)", ma.calls)
	}
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none: routing to a wired owner must not fail the run", errs)
	}
}

// spec: R-EXE-19
func TestScheduleFaults_ErrorRateFailsRunWhenTargetIsNotClassMock(t *testing.T) {
	// R-EXE-19 applies per target class, not just per verb: a mock applier
	// being wired at all does not make error_rate legal against a host that
	// isn't actually classified class: mock.
	f := config.Fault{
		Name:   "db_errors",
		At:     "t=0s",
		Target: "postgres:5432",
		Verb:   "error_rate",
		Inject: map[string]any{"error_rate": 0.15},
	}
	markers := make(chan PhaseMarker)
	close(markers)
	classes := map[string]egress.Class{"postgres:5432": egress.ClassInternal}
	ma := &fakeMockApplier{}

	done, _ := scheduleFaultsWithQueue([]config.Fault{f}, markers, time.Now(), &fault.Manager{}, &fakeApplier{}, nil, ma, classes)
	errs := <-done

	if ma.calls != 0 {
		t.Errorf("ApplyErrorRate calls = %d, want 0 — error_rate is not legal against a non-mock target even with a mock applier wired", ma.calls)
	}
	if len(errs) == 0 {
		t.Fatal("errs is empty, want an error: error_rate against a non-class:mock target must fail the run (R-EXE-19)")
	}
}

type fakeMockApplier struct {
	calls int
}

func (f *fakeMockApplier) ApplyErrorRate(faultName string, r realapplier.ErrorRate) (func() error, realapplier.ErrorRateApplied, error) {
	f.calls++
	return func() error { return nil }, realapplier.ErrorRateApplied{Requested: r.Rate, Applied: r.Rate}, nil
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
