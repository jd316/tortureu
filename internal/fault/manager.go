package fault

import (
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// Manager tracks applied faults so they can all be torn down together
// (R-EXE-5). Callers MUST defer m.Teardown() immediately after constructing
// a Manager and before applying any fault, so a panic partway through a run
// still triggers cleanup of everything applied before the panic:
//
//	m := &fault.Manager{}
//	defer m.Teardown()
//	// apply faults, tracking each one's undo via m.track (internal) through
//	// the Applier this package will grow once Toxiproxy/Docker clients exist
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
// implementation calls Toxiproxy's HTTP control API or execs into the
// target container via Docker. It is the seam internal/run (Task 7) wires
// a real client into; this package only depends on the interface so its
// tests never require a live daemon.
type Applier interface {
	// ApplyToxic applies a toxic to the network target and returns a func
	// that removes it.
	ApplyToxic(name string, t Toxic) (undo func() error, err error)
	// ApplyDocker performs the Docker action against its target container
	// (R-EXE-6) and returns a func that reverses it (unpause, stop the
	// stress-ng process, restore the previous cgroup limit, ...).
	ApplyDocker(name string, d DockerAction) (undo func() error, err error)
}

// Apply applies a translated Action via applier and tracks its undo so a
// later Teardown (including one run from a deferred recover, R-EXE-5)
// removes it. The fault's Name identifies it to the Applier and in any
// error Apply returns.
func (m *Manager) Apply(applier Applier, act Action) error {
	var undo func() error
	var err error
	switch act.Kind {
	case KindToxic:
		undo, err = applier.ApplyToxic(act.Fault.Name, *act.Toxic)
	case KindDocker:
		undo, err = applier.ApplyDocker(act.Fault.Name, *act.Docker)
	default:
		return errors.New("fault: Apply: Action has no toxic or docker payload")
	}
	if err != nil {
		return err
	}
	m.track(undo)
	return nil
}

// Teardown removes every fault applied so far, most-recently-applied first,
// and is safe to call multiple times (idempotent) and safe to call from a
// deferred recover after a panic (R-EXE-5). It continues past an undo that
// errors so one stuck fault never blocks the rest, and returns a joined
// error reporting every failure.
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
// SIGTERM (R-EXE-16) — the ordinary "developer hit Ctrl-C" and
// "orchestrator sent docker stop" abort paths, distinct from the in-process
// panic path TestManager_TeardownRunsOnPanicMidApply covers.
//
// It returns caught, which receives the signal once Teardown has finished
// running (the caller typically selects on it and calls os.Exit with an
// appropriate code), and stop, which disarms the handler and MUST be
// deferred by the caller once the run completes normally so this package
// stops intercepting the process's signals.
//
// SIGKILL cannot be caught — see the package doc comment. WatchSignals
// provides no protection against it.
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
