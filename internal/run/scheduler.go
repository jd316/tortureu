package run

import (
	"fmt"
	"strings"
	"sync"
	"time"

	realapplier "github.com/jd316/tortureu/internal/applier"
	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/egress"
	"github.com/jd316/tortureu/internal/fault"
	"github.com/jd316/tortureu/internal/queuefault"
)

// scheduleFaults is the "one clock" mechanism (R-SCOPE-2, R-EXE-1, R-EXE-8):
// every fault's at: is resolved against markers, k6's own phase-transition
// events, never against a wall clock this package keeps itself. t=<duration>
// faults are the one exception the grammar allows (R-CFG-11): they are
// absolute from runStart, which is a wall-clock instant, but only because
// the grammar itself declares that anchor as the escape hatch.
//
// Each per-fault apply runs in its own goroutine so a slow or blocking
// Applier call for one fault never delays another fault's anchor. Every
// such goroutine recovers its own panic and runs manager.Teardown() before
// re-arming nothing further — an uncaught panic in a goroutine terminates
// the whole process without running any other goroutine's deferred
// functions, so Run's own top-level recover cannot be relied on to reach
// code running here (R-EXE-5).
//
// fault.Manager's Apply/Teardown API tracks undo functions internally with
// no way to remove a single one early (only Teardown, which removes every
// tracked fault). R-CFG-10 says a fault's for: duration — when present —
// ends before the run does, which this package honors by calling the
// Applier directly for such faults (bypassing Manager) and running its own
// idempotent, mutex-guarded undo list, invoked by whichever runs first: the
// expiry timer, or Run's own final/teardown-on-exit path (see run.go's
// scheduleDone drain and manager.Teardown() call, and expireable.teardownAll
// wired into the same panic/signal paths below). This is a workaround for a
// gap in fault.Manager's public surface, not a design choice: an Apply that
// returned its own cancel func, or a Manager.Cancel(name), would remove the
// need for it. Escalated in the Task 7 report rather than edited in place,
// since internal/fault is read-only for this task.
// scheduleFaultsWithQueue is scheduleFaults with an explicit queue-fault
// route (R-EXE-19): queueApplier is internal/queuefault's real broker
// client, or nil when none is wired, in which case a poison_pill/duplicate
// fault fails the run rather than vanishing (see the KindPassed handling
// below). scheduleFaults is the production entry point and always threads
// deps.QueueApplier through; the split exists so tests can call this name
// directly without also constructing every other scheduleFaults argument.
func scheduleFaultsWithQueue(faults []config.Fault, markers <-chan PhaseMarker, runStart time.Time, manager *fault.Manager, applier fault.Applier, queueApplier queuefault.Applier, mockApplier MockApplier, classes map[string]egress.Class) (done <-chan []error, teardownExpiring func()) {
	doneCh := make(chan []error, 1)
	expiring := &expireTracker{}
	queueManager := &queuefault.Manager{}

	go func() {
		var wg sync.WaitGroup
		var errMu sync.Mutex
		var errs []error
		addErr := func(e error) {
			if e == nil {
				return
			}
			errMu.Lock()
			errs = append(errs, e)
			errMu.Unlock()
		}

		applyAt := func(f config.Fault, at time.Time) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						manager.Teardown()
						queueManager.Teardown()
						expiring.teardownAll()
						addErr(fmt.Errorf("fault %q: panic: %v", f.Name, r))
					}
				}()

				if d := time.Until(at); d > 0 {
					time.Sleep(d)
				}

				act, err := fault.Translate(f)
				if err != nil {
					addErr(fmt.Errorf("fault %q: %w", f.Name, err))
					return
				}
				if act.Kind == fault.KindToxic {
					registerToxicTarget(applier, f.Name, f.Target)
				}

				if act.Kind == fault.KindPassed {
					// R-EXE-19: a passed-over verb MUST be routed to its
					// owning layer, never silently dropped. If that layer
					// isn't actually wired to apply the effect, the run
					// MUST fail rather than proceed as if it had.
					if err := routePassedOver(act, mockApplier, classes, expiring, queueApplier, queueManager); err != nil {
						addErr(err)
					}
					return
				}

				if f.For == "" {
					// Lives until the run's final teardown.
					if err := manager.Apply(applier, act); err != nil {
						addErr(fmt.Errorf("fault %q: %w", f.Name, err))
					}
					return
				}

				dur, err := time.ParseDuration(f.For)
				if err != nil {
					addErr(fmt.Errorf("fault %q: invalid for: %q: %w", f.Name, f.For, err))
					return
				}
				undo, err := applyDirect(applier, act)
				if err != nil {
					addErr(fmt.Errorf("fault %q: %w", f.Name, err))
					return
				}
				once := onceUndo(undo)
				expiring.add(once)
				time.AfterFunc(dur, func() { once() })
			}()
		}

		var phaseAnchored []config.Fault
		for _, f := range faults {
			if strings.HasPrefix(f.At, "t=") {
				d, err := time.ParseDuration(strings.TrimPrefix(f.At, "t="))
				if err != nil {
					addErr(fmt.Errorf("fault %q: invalid at: %q: %w", f.Name, f.At, err))
					continue
				}
				applyAt(f, runStart.Add(d))
				continue
			}
			phaseAnchored = append(phaseAnchored, f)
		}

		for len(phaseAnchored) > 0 {
			m, ok := <-markers
			if !ok {
				// R-EXE-19's own worst failure mode: a declared fault that
				// never fires must never disappear silently. Every fault
				// still waiting for a phase that will now never start (the
				// load ended first) is recorded as a failure, not dropped.
				for _, f := range phaseAnchored {
					addErr(fmt.Errorf("fault %q: load ended before phase %q started — the fault never fired (R-EXE-19)", f.Name, f.At))
				}
				break
			}
			var remaining []config.Fault
			for _, f := range phaseAnchored {
				phase, offset, hasOffset := strings.Cut(f.At, "+")
				if phase != m.Phase {
					remaining = append(remaining, f)
					continue
				}
				at := m.At
				if hasOffset {
					d, err := time.ParseDuration(offset)
					if err != nil {
						addErr(fmt.Errorf("fault %q: invalid at: %q: %w", f.Name, f.At, err))
						continue
					}
					at = at.Add(d)
				}
				applyAt(f, at)
			}
			phaseAnchored = remaining
		}

		wg.Wait()
		doneCh <- errs
	}()

	return doneCh, func() {
		expiring.teardownAll()
		queueManager.Teardown()
	}
}

// scheduleFaults is the production entry point: it always routes through
// scheduleFaultsWithQueue, threading Deps.Applier's queue and mock
// counterparts through so error_rate/poison_pill/duplicate faults are
// routed to their owner rather than silently skipped (R-EXE-19).
func scheduleFaults(faults []config.Fault, markers <-chan PhaseMarker, runStart time.Time, manager *fault.Manager, applier fault.Applier, queueApplier queuefault.Applier, mockApplier MockApplier, classes map[string]egress.Class) (done <-chan []error, teardownExpiring func()) {
	return scheduleFaultsWithQueue(faults, markers, runStart, manager, applier, queueApplier, mockApplier, classes)
}

// routePassedOver sends a KindPassed action to its owning layer (R-EXE-15's
// table, mirrored in fault.Translate's otherLayerOwner) and returns an error
// if that layer either rejects the fault or has no apply capability wired
// (R-EXE-19: a run that cannot apply a declared fault must not proceed as if
// it had). expiring tracks the undo for owners with no Manager of their own
// (WireMockApplier — a real, reversible undo per R-EXE-18), the same tracker
// applyDirect uses for for:-duration Toxiproxy/Docker faults, so it is
// covered by the same panic/signal/final teardown paths.
func routePassedOver(act fault.Action, mockApplier MockApplier, classes map[string]egress.Class, expiring *expireTracker, queueApplier queuefault.Applier, queueManager *queuefault.Manager) error {
	f := act.Fault
	switch act.Owner {
	case "internal/egress":
		if err := egress.ValidateErrorRate(f.Name, f.Inject); err != nil {
			return fmt.Errorf("fault %q: %w", f.Name, err)
		}
		// R-EXE-19 applies per target class, not just per verb: error_rate
		// is only legal against a host actually classified class: mock
		// (R-EXE-15) — a wired mock applier does not make it legal against
		// anything else.
		if classes[f.Target] != egress.ClassMock {
			return fmt.Errorf("fault %q: error_rate targets %q, which is not classified class: mock — failing rather than silently skipping (R-EXE-19)", f.Name, f.Target)
		}
		if mockApplier == nil {
			return fmt.Errorf("fault %q: error_rate is validated by internal/egress but no mock-provider client is wired — failing rather than silently skipping (R-EXE-19)", f.Name)
		}
		er, err := realapplier.TranslateErrorRate(f)
		if err != nil {
			return fmt.Errorf("fault %q: %w", f.Name, err)
		}
		undo, applied, err := mockApplier.ApplyErrorRate(f.Name, er)
		if err != nil {
			return fmt.Errorf("fault %q: %w", f.Name, err)
		}
		expiring.add(onceUndo(undo))
		if applied.Approximated {
			// R-EXE-22: report the rounding rather than let it reach a user
			// silently — this is not a failure, so it travels back as an
			// approximationNote Run recognizes and reroutes to a verdict
			// warning instead of failing the run over it.
			return approximationNote{msg: fmt.Sprintf(
				"fault %q: error_rate requested %.3f, applied %.3f (rounded to WireMock's cycle resolution, R-EXE-22)",
				f.Name, applied.Requested, applied.Applied)}
		}
		return nil

	case "internal/queuefault":
		if queueApplier == nil {
			return fmt.Errorf("fault %q: verb %q is owned by internal/queuefault but no broker client is wired — failing rather than silently skipping (R-EXE-19)", f.Name, f.Verb)
		}
		qact, err := queuefault.Translate(f)
		if err != nil {
			return fmt.Errorf("fault %q: %w", f.Name, err)
		}
		if err := queueManager.Apply(queueApplier, qact); err != nil {
			return fmt.Errorf("fault %q: %w", f.Name, err)
		}
		return nil

	default:
		return fmt.Errorf("fault %q: verb %q has no wired owner — failing rather than silently skipping (R-EXE-19)", f.Name, f.Verb)
	}
}

// registerToxicTarget tells applier (if it supports RegisterTarget — see
// ToxiproxyApplier's doc comment) which upstream host:port faultName's
// toxic applies to. fault.Applier.ApplyToxic is not given the target
// directly (fault.Toxic has no such field), so this is how internal/run
// bridges config.Fault.Target across that gap without editing internal/fault.
func registerToxicTarget(applier fault.Applier, faultName, target string) {
	if r, ok := applier.(interface{ RegisterTarget(string, string) }); ok {
		r.RegisterTarget(faultName, target)
	}
}

// applyDirect calls the Applier for act without going through
// fault.Manager, returning its undo directly. Used only for faults with a
// for: duration (see scheduleFaults's doc comment for why).
func applyDirect(applier fault.Applier, act fault.Action) (func() error, error) {
	switch act.Kind {
	case fault.KindToxic:
		return applier.ApplyToxic(act.Fault.Name, *act.Toxic)
	case fault.KindDocker:
		return applier.ApplyDocker(act.Fault.Name, *act.Docker)
	default:
		return nil, fmt.Errorf("fault: applyDirect: Action has no toxic or docker payload")
	}
}

// onceUndo wraps undo so it is safe to invoke from both an expiry timer and
// a teardown-on-exit path racing each other; the underlying side effect
// runs at most once.
func onceUndo(undo func() error) func() error {
	var once sync.Once
	var err error
	return func() error {
		once.Do(func() { err = undo() })
		return err
	}
}

// approximationNote is a non-fatal note carried through the same error
// channel scheduleFaults uses for real failures (R-EXE-22): WireMock's
// error_rate mechanism can only hit certain rates exactly
// (errorRateCycleLen states), and a caller tuning a threshold needs to know
// when a requested rate got rounded. It implements error only so it can
// travel through addErr/errs without a second channel; Run splits it back
// out (by type) before deciding whether the run failed, and surfaces its
// message as a verdict warning instead (see run.go).
type approximationNote struct{ msg string }

func (n approximationNote) Error() string { return n.msg }

// expireTracker holds the once-wrapped undo funcs for faults applied
// outside fault.Manager (see scheduleFaults), so Run's panic/signal paths
// can tear them down too.
type expireTracker struct {
	mu    sync.Mutex
	undos []func() error
}

func (t *expireTracker) add(undo func() error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.undos = append(t.undos, undo)
}

func (t *expireTracker) teardownAll() {
	t.mu.Lock()
	undos := t.undos
	t.undos = nil
	t.mu.Unlock()
	for i := len(undos) - 1; i >= 0; i-- {
		undos[i]()
	}
}
