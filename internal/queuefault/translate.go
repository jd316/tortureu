// Package queuefault translates config.Fault into broker-producer actions
// for the two verbs R-EXE-15 assigns to it: poison_pill and duplicate
// (SPEC.md §4.4 / R-CFG-14, RESEARCH.md §18). It mirrors internal/fault's
// shape — a pure Translate producing action data, a Manager that tracks
// applied actions for teardown, and an Applier seam so translation is
// testable without a live broker — rather than inventing a second style.
//
// Teardown limit (R-EXE-5, R-EXE-16): a poison-pill message, once produced,
// is durably appended to the topic's log, and a duplicate, once
// re-delivered, has already been re-delivered and possibly consumed. There
// is no "un-publish" in a broker's replicated log the way there is a
// "remove this toxic" in Toxiproxy or a "SIGCONT" for a paused container.
// Manager.Teardown here can only stop an in-progress injection from sending
// anything further (e.g. cancel a duplicate-redelivery loop before its next
// copy goes out, or halt a multi-message poison_pill batch before later
// messages are produced) — it cannot retract a message already on the
// topic, and it cannot undo a side effect a consumer already produced from
// one. Callers MUST NOT treat a successful Teardown as evidence the queue
// was returned to its pre-fault state; it was not, and cannot be. This is
// the same posture internal/fault takes on SIGKILL: document the limit
// rather than imply protection the package cannot provide.
package queuefault

import (
	"fmt"

	"github.com/jd316/tortureu/internal/config"
)

// ActionKind distinguishes the two verbs this package owns (R-EXE-15).
type ActionKind string

const (
	KindPoisonPill ActionKind = "poison_pill"
	KindDuplicate  ActionKind = "duplicate"
)

// PoisonPill is a malformed-message injection against a queue target
// (SPEC.md §4.4). Count is how many malformed messages to produce.
type PoisonPill struct {
	Topic string
	Count int
}

// Duplicate is a proportional re-delivery injection against a queue target
// (SPEC.md §4.4). Rate is a fraction of messages re-delivered (e.g. 0.05
// for 5%), not a count.
type Duplicate struct {
	Topic string
	Rate  float64
}

// Action is one translated queue fault.
type Action struct {
	Fault      config.Fault
	Kind       ActionKind
	PoisonPill *PoisonPill
	Duplicate  *Duplicate
}

// verbModifiers is this package's slice of the SPEC.md §4.4 / R-CFG-14
// table: the two verbs R-EXE-15 assigns to internal/queuefault and the
// modifiers each owns. Duplicated from internal/config for the same reason
// internal/fault duplicates it: config's table is unexported, and Translate
// must defend against a Fault built directly rather than via config.Parse.
var verbModifiers = map[string]map[string]bool{
	"poison_pill": {"count": true},
	"duplicate":   {},
}

// defaultPoisonPillCount is used when count: is absent. SPEC.md's §4.4
// table lists count as poison_pill's modifier without stating a required
// value or a default when omitted; RESEARCH.md §18 frames the failure mode
// as "one malformed message [that] blocks an entire partition", so 1 is the
// smallest injection that still demonstrates the failure mode. SPEC.md does
// not state this default explicitly — flagged as a gap in task-5b-report.md
// per R-PROC-2/4.
const defaultPoisonPillCount = 1

// Translate converts one parsed fault into a PoisonPill or Duplicate
// action. Unlike internal/fault.Translate, it does not pass over verbs it
// does not own: R-EXE-15's table names this package as the terminal owner
// of poison_pill and duplicate, so an unrecognized verb here is simply
// wrong — either misrouted by the caller or not a real verb at all — and
// Translate rejects it rather than inventing a further layer to pass to.
func Translate(f config.Fault) (Action, error) {
	allowed, known := verbModifiers[f.Verb]
	if !known {
		return Action{}, fmt.Errorf("fault %q: %q is not a verb internal/queuefault owns (R-EXE-15: poison_pill, duplicate only)", f.Name, f.Verb)
	}
	for k := range f.Inject {
		if k == f.Verb {
			continue
		}
		if !allowed[k] {
			return Action{}, fmt.Errorf("fault %q: %q is not a valid modifier for verb %q", f.Name, k, f.Verb)
		}
	}
	if f.Target == "" {
		return Action{}, fmt.Errorf("fault %q: target is required", f.Name)
	}

	switch f.Verb {
	case "poison_pill":
		count := defaultPoisonPillCount
		if raw, ok := f.Inject["count"]; ok {
			n, err := toInt(raw)
			if err != nil {
				return Action{}, fmt.Errorf("fault %q: count: %w", f.Name, err)
			}
			count = n
		}
		if count < 1 {
			return Action{}, fmt.Errorf("fault %q: count: %d is out of range, legal range is integer >= 1 (R-CFG-23)", f.Name, count)
		}
		return Action{Fault: f, Kind: KindPoisonPill, PoisonPill: &PoisonPill{Topic: f.Target, Count: count}}, nil
	case "duplicate":
		raw, ok := f.Inject["duplicate"]
		if !ok {
			return Action{}, fmt.Errorf("fault %q: duplicate: missing rate", f.Name)
		}
		rate, err := toFloat(raw)
		if err != nil {
			return Action{}, fmt.Errorf("fault %q: duplicate: %w", f.Name, err)
		}
		if rate < 0.0 || rate > 1.0 {
			return Action{}, fmt.Errorf("fault %q: duplicate: %v is out of range, legal range is 0.0 … 1.0 (R-CFG-23; a proportion, not a multiplier)", f.Name, rate)
		}
		return Action{Fault: f, Kind: KindDuplicate, Duplicate: &Duplicate{Topic: f.Target, Rate: rate}}, nil
	default:
		// Unreachable: verbModifiers only contains the two cases above.
		return Action{}, fmt.Errorf("fault %q: %q is not a supported queue verb", f.Name, f.Verb)
	}
}

func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case float64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("expected a number, got %T", v)
	}
}

func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	default:
		return 0, fmt.Errorf("expected a number, got %T", v)
	}
}
