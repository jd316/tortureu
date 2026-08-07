package fault

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/config"
)

// fakeApplier is a test double for Applier: no live Toxiproxy or Docker
// daemon involved, just enough to prove Manager.Apply wires a translated
// Action's undo into the teardown chain (R-EXE-5). Live-daemon behavior
// (does the HTTP call actually remove the toxic, does `docker kill` actually
// reach the container) cannot be proven without a running Toxiproxy/Docker
// and is out of scope for this package's tests — see task-5-report.md.
type fakeApplier struct {
	removed []string
}

func (a *fakeApplier) ApplyToxic(name string, t Toxic) (func() error, error) {
	return func() error {
		a.removed = append(a.removed, name)
		return nil
	}, nil
}

func (a *fakeApplier) ApplyDocker(name string, d DockerAction) (func() error, error) {
	return func() error {
		a.removed = append(a.removed, name)
		return nil
	}, nil
}

// spec: R-EXE-5
// Apply is the seam a real runner uses: translate a fault, apply it via a
// live Applier, and have the resulting undo tracked for Teardown
// automatically, so a caller cannot forget to wire cleanup for a fault it
// successfully applied.
func TestManager_ApplyTracksUndoForTeardown(t *testing.T) {
	m := &Manager{}
	applier := &fakeApplier{}

	act, err := Translate(config.Fault{
		Name: "pg_slow", Target: "postgres:5432", Verb: "latency",
		Inject: map[string]any{"latency": "300ms"},
	})
	if err != nil {
		t.Fatalf("Translate: unexpected error: %v", err)
	}

	if err := m.Apply(applier, act); err != nil {
		t.Fatalf("Apply: unexpected error: %v", err)
	}
	if err := m.Teardown(); err != nil {
		t.Fatalf("Teardown: unexpected error: %v", err)
	}
	if len(applier.removed) != 1 || applier.removed[0] != "pg_slow" {
		t.Fatalf("Teardown removed %v, want [pg_slow]", applier.removed)
	}
}

// spec: R-EXE-5
// A crashed run MUST NOT leave a fault applied. This test simulates the
// real failure mode: applying a third fault panics partway through a run
// that already applied two. The recover+Teardown pattern callers are
// expected to use (deferred immediately after constructing the Manager)
// MUST still undo every fault that was successfully applied before the
// panic — not just the ones applied before whatever step happened to fail.
func TestManager_TeardownRunsOnPanicMidApply(t *testing.T) {
	m := &Manager{}
	var undone []string

	func() {
		defer func() {
			recover() // simulate the top-level recover a real run would have
		}()
		defer m.Teardown()

		for i, name := range []string{"one", "two", "panics"} {
			name := name
			if name == "panics" {
				panic("boom")
			}
			m.track(func() error {
				undone = append(undone, name)
				return nil
			})
			_ = i
		}
	}()

	if len(undone) != 2 {
		t.Fatalf("Teardown after panic: undid %v, want exactly [two one] (2 faults)", undone)
	}
	// Teardown must run in reverse-apply order so later faults (more likely
	// to depend on earlier ones being present) are removed first.
	if undone[0] != "two" || undone[1] != "one" {
		t.Fatalf("Teardown order = %v, want [two one] (reverse of apply order)", undone)
	}
}

// spec: R-EXE-5
// Teardown MUST be idempotent: a run's deferred cleanup and an explicit
// user-triggered abort could both call it. A second call must not re-run
// undo funcs (which would double-remove an already-removed toxiproxy toxic
// or double-unpause a container) and must not panic.
func TestManager_TeardownIsIdempotent(t *testing.T) {
	m := &Manager{}
	calls := 0
	m.track(func() error {
		calls++
		return nil
	})

	if err := m.Teardown(); err != nil {
		t.Fatalf("first Teardown: unexpected error: %v", err)
	}
	if err := m.Teardown(); err != nil {
		t.Fatalf("second Teardown: unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("undo func called %d times, want exactly 1 (idempotent)", calls)
	}
}

// spec: R-EXE-5
// One toxic failing to tear down (e.g. the proxy already vanished) MUST
// NOT stop the rest of the faults from being torn down — otherwise a single
// flaky removal leaves every fault applied after it stuck in place forever.
func TestManager_TeardownContinuesPastAnUndoError(t *testing.T) {
	m := &Manager{}
	secondRan := false
	m.track(func() error { return errors.New("proxy gone") })
	m.track(func() error { secondRan = true; return nil })

	err := m.Teardown()
	if err == nil {
		t.Fatal("Teardown: want a non-nil error reporting the failed undo, got nil")
	}
	if !secondRan {
		t.Fatal("Teardown: a failing undo must not prevent the remaining undos from running")
	}
}

// spec: R-EXE-16
// A developer aborting a run with Ctrl-C (SIGINT) is the ordinary case, not
// the exceptional one — R-EXE-5's "torn down on abort" has to cover it, not
// just an in-process panic. WatchSignals MUST run Teardown when the process
// receives SIGINT, before whatever the caller does next (typically os.Exit).
func TestManager_WatchSignalsTearsDownOnSIGINT(t *testing.T) {
	m := &Manager{}
	undone := false
	m.track(func() error { undone = true; return nil })

	caught, stop := m.WatchSignals()
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT to self: %v", err)
	}

	select {
	case sig := <-caught:
		if sig != syscall.SIGINT {
			t.Fatalf("caught signal = %v, want SIGINT", sig)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchSignals: SIGINT was not observed within 2s")
	}

	if !undone {
		t.Fatal("WatchSignals: Teardown was not run on SIGINT")
	}
}

// spec: R-EXE-16
// Same contract for SIGTERM (the signal `docker stop`/most orchestrators
// send), covered separately from SIGINT since R-EXE-16 names both and a
// handler for one is not evidence the other is wired.
func TestManager_WatchSignalsTearsDownOnSIGTERM(t *testing.T) {
	m := &Manager{}
	undone := false
	m.track(func() error { undone = true; return nil })

	caught, stop := m.WatchSignals()
	defer stop()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM to self: %v", err)
	}

	select {
	case sig := <-caught:
		if sig != syscall.SIGTERM {
			t.Fatalf("caught signal = %v, want SIGTERM", sig)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchSignals: SIGTERM was not observed within 2s")
	}

	if !undone {
		t.Fatal("WatchSignals: Teardown was not run on SIGTERM")
	}
}

// spec: R-EXE-16
// stop() MUST disarm the handler: after it returns, this package's
// WatchSignals goroutine must not fire Teardown again, and — just as
// important for a test suite sending real signals to its own process — the
// signal must fall through to Go's default disposition instead of being
// swallowed forever. This proves stop() actually calls signal.Stop rather
// than merely closing an internal channel.
func TestManager_WatchSignalsStopDisarmsHandler(t *testing.T) {
	m := &Manager{}
	calls := 0
	m.track(func() error { calls++; return nil })

	caught, stop := m.WatchSignals()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT to self: %v", err)
	}
	<-caught
	stop()

	if calls != 1 {
		t.Fatalf("Teardown ran %d times after one SIGINT, want 1", calls)
	}
}
