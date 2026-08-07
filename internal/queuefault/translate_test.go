package queuefault

import (
	"testing"

	"github.com/jdb316/tortureu/internal/config"
)

// spec: R-EXE-15
// This package owns exactly poison_pill and duplicate (SPEC.md §8's
// ownership table). Translate MUST refuse any other verb: there is no
// further layer for this package to pass to, so unlike internal/fault
// (which must pass over verbs it does not own to whatever layer does),
// rejection here is correct — this is the terminal owner named in
// R-EXE-15's table for these two verbs and nothing else.
func TestTranslate_RejectsVerbItDoesNotOwn(t *testing.T) {
	f := config.Fault{
		Name: "slow_pg", Target: "postgres:5432", Verb: "latency",
		Inject: map[string]any{"latency": "300ms"},
	}
	if _, err := Translate(f); err == nil {
		t.Fatal("Translate: want error for a verb owned by internal/fault, got nil")
	}
}

// spec: R-CFG-14
// Translate re-validates the verb/modifier pairing itself rather than
// trusting the caller went through config.Parse, the same defense-in-depth
// internal/fault.Translate applies. count is poison_pill's only modifier;
// any other key must be rejected.
func TestTranslate_RejectsModifierNotOwnedByVerb(t *testing.T) {
	f := config.Fault{
		Name: "bad", Target: "orders_queue", Verb: "duplicate",
		Inject: map[string]any{"duplicate": 0.1, "count": 3},
	}
	if _, err := Translate(f); err == nil {
		t.Fatal("Translate: want error for count modifier on duplicate verb, got nil")
	}
}

// spec: R-CFG-10
// Every fault carries a target (R-CFG-10); for a queue fault that target is
// the topic/queue name. Translate MUST require it rather than producing an
// action with nowhere to publish to.
func TestTranslate_RequiresTarget(t *testing.T) {
	f := config.Fault{
		Name: "no_target", Verb: "duplicate",
		Inject: map[string]any{"duplicate": 0.1},
	}
	if _, err := Translate(f); err == nil {
		t.Fatal("Translate: want error for missing target, got nil")
	}
}

// spec: R-CFG-14
// poison_pill's effect (SPEC.md §4.4 table) is "malformed message"; count
// is its optional modifier. Translate MUST carry the target queue and the
// requested count through to a PoisonPill action.
func TestTranslate_PoisonPillCarriesTargetAndCount(t *testing.T) {
	f := config.Fault{
		Name: "bad_message", Target: "orders_queue", Verb: "poison_pill",
		Inject: map[string]any{"poison_pill": true, "count": 3},
	}
	act, err := Translate(f)
	if err != nil {
		t.Fatalf("Translate: unexpected error: %v", err)
	}
	if act.Kind != KindPoisonPill {
		t.Fatalf("Kind = %v, want KindPoisonPill", act.Kind)
	}
	if act.PoisonPill == nil {
		t.Fatal("PoisonPill payload is nil")
	}
	if act.PoisonPill.Topic != "orders_queue" {
		t.Fatalf("Topic = %q, want %q", act.PoisonPill.Topic, "orders_queue")
	}
	if act.PoisonPill.Count != 3 {
		t.Fatalf("Count = %d, want 3", act.PoisonPill.Count)
	}
}

// spec: R-CFG-14
// count is documented as optional (SPEC.md §4.4 lists it as poison_pill's
// modifier, not a required field), and RESEARCH.md §18 frames the failure
// mode as "one malformed message" blocking a partition. Absent an explicit
// count, Translate defaults to a single message — the smallest injection
// that still blocks a partition, per RESEARCH.md §18. SPEC.md does not
// state this default explicitly; see task-5b-report.md for the escalation.
func TestTranslate_PoisonPillDefaultsCountToOne(t *testing.T) {
	f := config.Fault{
		Name: "bad_message", Target: "orders_queue", Verb: "poison_pill",
		Inject: map[string]any{"poison_pill": true},
	}
	act, err := Translate(f)
	if err != nil {
		t.Fatalf("Translate: unexpected error: %v", err)
	}
	if act.PoisonPill.Count != 1 {
		t.Fatalf("Count = %d, want default of 1", act.PoisonPill.Count)
	}
}

// spec: R-CFG-14
// duplicate's effect (SPEC.md §4.4 table) is "fraction redelivered": the
// value is a rate (e.g. 0.05), not a count of messages. Translate MUST
// carry that rate through as a float, not coerce it into a message count.
func TestTranslate_DuplicateCarriesTargetAndRate(t *testing.T) {
	f := config.Fault{
		Name: "dup_message", Target: "orders_queue", Verb: "duplicate",
		Inject: map[string]any{"duplicate": 0.05},
	}
	act, err := Translate(f)
	if err != nil {
		t.Fatalf("Translate: unexpected error: %v", err)
	}
	if act.Kind != KindDuplicate {
		t.Fatalf("Kind = %v, want KindDuplicate", act.Kind)
	}
	if act.Duplicate == nil {
		t.Fatal("Duplicate payload is nil")
	}
	if act.Duplicate.Topic != "orders_queue" {
		t.Fatalf("Topic = %q, want %q", act.Duplicate.Topic, "orders_queue")
	}
	if act.Duplicate.Rate != 0.05 {
		t.Fatalf("Rate = %v, want 0.05", act.Duplicate.Rate)
	}
}

// spec: R-EXE-15
// The pass-over shape internal/fault.Translate produces for these two verbs
// (Owner: "internal/queuefault") must actually be routable to this
// package's own Translate without a second translation of the verb table —
// i.e. this package's Translate must accept the exact same config.Fault
// values internal/fault passed over rather than erroring on them.
func TestTranslate_AcceptsFaultsInternalFaultPassesOver(t *testing.T) {
	cases := []config.Fault{
		{Name: "bad_message", Target: "orders_queue", Verb: "poison_pill", Inject: map[string]any{"poison_pill": true, "count": 3}},
		{Name: "dup_message", Target: "orders_queue", Verb: "duplicate", Inject: map[string]any{"duplicate": 0.1}},
	}
	for _, f := range cases {
		if _, err := Translate(f); err != nil {
			t.Fatalf("Translate(%s): unexpected error: %v", f.Name, err)
		}
	}
}
