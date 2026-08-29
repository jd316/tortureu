package queuefault

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/jd316/TortureU/internal/config"
)

// fakeApplier is a test double for Applier: no live broker involved, just
// enough to prove Manager.Apply wires a translated Action's undo into the
// teardown chain (R-EXE-5). Whether the undo actually stops a live
// producer goroutine, or whether a poison pill actually reaches a real
// topic, cannot be proven without a running broker and is out of scope for
// this package's tests — see task-5b-report.md.
type fakeApplier struct {
	removed []string
}

func (a *fakeApplier) ApplyPoisonPill(name string, p PoisonPill) (func() error, error) {
	return func() error {
		a.removed = append(a.removed, name)
		return nil
	}, nil
}

func (a *fakeApplier) ApplyDuplicate(name string, d Duplicate) (func() error, error) {
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
		Name: "dup_message", Target: "orders_queue", Verb: "duplicate",
		Inject: map[string]any{"duplicate": 0.05},
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
	if len(applier.removed) != 1 || applier.removed[0] != "dup_message" {
		t.Fatalf("Teardown removed %v, want [dup_message]", applier.removed)
	}
}

// spec: R-EXE-5
// A crashed run MUST NOT leave a fault applied (or, for a queue fault, must
// stop it from continuing to inject — see the package doc for what Teardown
// cannot undo). This simulates the real failure mode: applying a third
// fault panics partway through a run that already applied two. The
// recover+Teardown pattern MUST still undo every fault successfully applied
// before the panic, in reverse order.
func TestManager_TeardownRunsOnPanicMidApply(t *testing.T) {
	m := &Manager{}
	var undone []string

	func() {
		defer func() {
			recover()
		}()
		defer m.Teardown()

		for _, name := range []string{"one", "two", "panics"} {
			name := name
			if name == "panics" {
				panic("boom")
			}
			m.track(func() error {
				undone = append(undone, name)
				return nil
			})
		}
	}()

	if len(undone) != 2 {
		t.Fatalf("Teardown after panic: undid %v, want exactly [two one] (2 faults)", undone)
	}
	if undone[0] != "two" || undone[1] != "one" {
		t.Fatalf("Teardown order = %v, want [two one] (reverse of apply order)", undone)
	}
}

// spec: R-EXE-5
// Teardown MUST be idempotent: a run's deferred cleanup and an explicit
// user-triggered abort could both call it. A second call must not re-run
// undo funcs and must not panic.
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
// One undo failing (e.g. the producer connection already dropped) MUST NOT
// stop the rest of the faults from being torn down.
func TestManager_TeardownContinuesPastAnUndoError(t *testing.T) {
	m := &Manager{}
	secondRan := false
	m.track(func() error { return errors.New("producer gone") })
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
// A developer aborting a run with Ctrl-C (SIGINT) is the ordinary case.
// WatchSignals MUST run Teardown when the process receives SIGINT, before
// whatever the caller does next.
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
// Same contract for SIGTERM, covered separately since R-EXE-16 names both
// and a handler for one is not evidence the other is wired.
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
// stop() MUST disarm the handler so the goroutine does not fire Teardown
// again and the signal falls through to Go's default disposition instead
// of being swallowed forever.
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
