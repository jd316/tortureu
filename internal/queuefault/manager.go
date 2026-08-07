package queuefault

import (
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Manager tracks applied queue faults so they can all be torn down together
// (R-EXE-5), subject to the package-doc limit: for these two verbs,
// "torn down" means "stopped from injecting further", not "reverted".
// Callers MUST defer m.Teardown() immediately after constructing a Manager
// and before applying any fault, so a panic partway through a run still
// triggers cleanup of everything applied before the panic.
type Manager struct {
	mu    sync.Mutex
	undos []func() error
}

// track records an undo function for a successfully applied fault.
func (m *Manager) track(undo func() error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.undos = append(m.undos, undo)
}

// Applier performs the live side effect for a translated Action: a real
// implementation produces a malformed message onto the target topic, or
// starts a consumer-side/producer-side re-delivery loop for duplicate. It
// is the seam internal/run wires a real broker client into, so this
// package's tests never require a live Kafka/broker. The returned undo
// stops further injection (see the package doc); it does not retract
// anything already produced or re-delivered.
type Applier interface {
	// ApplyPoisonPill produces the malformed message(s) and returns a func
	// that stops any further ones from being produced (a no-op if the
	// batch already completed — there is nothing left to stop).
	ApplyPoisonPill(name string, p PoisonPill) (undo func() error, err error)
	// ApplyDuplicate starts re-delivering a fraction of messages and
	// returns a func that halts the redelivery loop before its next copy.
	ApplyDuplicate(name string, d Duplicate) (undo func() error, err error)
}

// Apply applies a translated Action via applier and tracks its undo so a
// later Teardown (including one run from a deferred recover, R-EXE-5)
// stops it. The fault's Name identifies it to the Applier and in any error
// Apply returns.
func (m *Manager) Apply(applier Applier, act Action) error {
	var undo func() error
	var err error
	switch act.Kind {
	case KindPoisonPill:
		undo, err = applier.ApplyPoisonPill(act.Fault.Name, *act.PoisonPill)
	case KindDuplicate:
		undo, err = applier.ApplyDuplicate(act.Fault.Name, *act.Duplicate)
	default:
		return errors.New("queuefault: Apply: Action has no poison_pill or duplicate payload")
	}
	if err != nil {
		return err
	}
	m.track(undo)
	return nil
}

// Teardown stops every fault applied so far from injecting further,
// most-recently-applied first, and is safe to call multiple times
// (idempotent) and safe to call from a deferred recover after a panic
// (R-EXE-5). It continues past an undo that errors so one stuck fault never
// blocks the rest, and returns a joined error reporting every failure. See
// the package doc: this does not retract messages already on the topic.
func (m *Manager) Teardown() error {
	m.mu.Lock()
	undos := m.undos
	m.undos = nil
	m.mu.Unlock()

	var errs []error
	for i := len(undos) - 1; i >= 0; i-- {
		if err := undos[i](); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// WatchSignals arms Teardown to run when the process receives SIGINT or
// SIGTERM (R-EXE-16). It returns caught, which receives the signal once
// Teardown has finished running, and stop, which disarms the handler and
// MUST be deferred by the caller once the run completes normally.
//
// SIGKILL cannot be caught — see the package doc. WatchSignals provides no
// protection against it, and even a caught signal only stops further
// injection; it cannot undo a poison pill or duplicate already produced.
func (m *Manager) WatchSignals() (caught <-chan os.Signal, stop func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	out := make(chan os.Signal, 1)
	stopped := make(chan struct{})
	go func() {
		select {
		case sig := <-sigCh:
			m.Teardown()
			out <- sig
		case <-stopped:
		}
	}()

	var stopOnce sync.Once
	return out, func() {
		stopOnce.Do(func() {
			signal.Stop(sigCh)
			close(stopped)
		})
	}
}
