package run

import (
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/fault"
)

// spec: R-SCOPE-2
// spec: R-EXE-1
func TestScheduleFaults_WaitsForPhaseMarkerNotWallClock(t *testing.T) {
	f := config.Fault{Name: "f1", At: "peak", Target: "checkout-api", Verb: "pause", Inject: map[string]any{"pause": true}}
	markers := make(chan PhaseMarker)
	applier := &trackingApplier{}
	manager := &fault.Manager{}

	done, teardown := scheduleFaults([]config.Fault{f}, markers, time.Now(), manager, applier, nil)
	defer teardown()

	// Give the scheduler time to (wrongly) fire on a wall-clock timer if it
	// had one; it must not, since "peak" has not started yet on k6's clock.
	time.Sleep(50 * time.Millisecond)
	if applier.applyCalls != 0 {
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
	if applier.applyCalls != 1 {
		t.Errorf("applyCalls = %d, want 1 after the peak marker arrived", applier.applyCalls)
	}
}
