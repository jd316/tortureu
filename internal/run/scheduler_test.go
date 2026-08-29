package run

import (
	"testing"
	"time"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/fault"
)

// spec: R-SCOPE-2
// spec: R-EXE-1
func TestScheduleFaults_WaitsForPhaseMarkerNotWallClock(t *testing.T) {
	f := config.Fault{Name: "f1", At: "peak", Target: "checkout-api", Verb: "pause", Inject: map[string]any{"pause": true}}
	markers := make(chan PhaseMarker)
	applier := &trackingApplier{}
	manager := &fault.Manager{}

	done, teardown := scheduleFaults([]config.Fault{f}, markers, time.Now(), manager, applier, nil, nil, nil)
	defer teardown()

	// Give the scheduler time to (wrongly) fire on a wall-clock timer if it
	// had one; it must not, since "peak" has not started yet on k6's clock.
	time.Sleep(50 * time.Millisecond)
	if applier.applyCalls.Load() != 0 {
		t.Fatal("fault applied before its phase marker arrived — R-EXE-1/R-EXE-8 require anchoring to k6's own clock, not a wall-clock guess")
	}

	// Now k6's clock actually reaches "peak" — the marker is the only signal
	// that should trigger application.
	markers <- PhaseMarker{Phase: "peak", At: time.Now()}
	close(markers)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduleFaults never completed after the marker arrived")
	}
	if applier.applyCalls.Load() != 1 {
		t.Errorf("applyCalls = %d, want 1 after the peak marker arrived", applier.applyCalls.Load())
	}
}

// spec: R-EXE-19
func TestScheduleFaults_RecordsErrorWhenLoadEndsBeforePhaseAnchorFires(t *testing.T) {
	// R-EXE-19's own stated worst failure mode: "a declared fault that
	// never fires; the run completes, the verdict reads pass, and the user
	// concludes their system withstood a fault that was never applied."
	// This reproduces it one branch over from where it was fixed for
	// passed-over verbs: a phase-anchored fault whose phase never starts
	// because the load ended first (markers channel closes) must not
	// vanish silently.
	f := config.Fault{Name: "spike_pause", At: "spike", Target: "checkout-api", Verb: "pause", Inject: map[string]any{"pause": true}}
	markers := make(chan PhaseMarker)
	close(markers) // load ended; "spike" never started

	done, teardown := scheduleFaults([]config.Fault{f}, markers, time.Now(), &fault.Manager{}, &trackingApplier{}, nil, nil, nil)
	defer teardown()

	errs := <-done
	if len(errs) == 0 {
		t.Fatal("errs is empty, want an error naming spike_pause — a fault whose phase never started must not disappear silently (R-EXE-19)")
	}
}
