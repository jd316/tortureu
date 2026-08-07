package fault

import (
	"os"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/config"
)

// spec: R-CFG-14
// Translate is a second line of defense: config.Parse already rejects two
// verbs or a mismatched modifier, but Fault values can be constructed
// directly (tests, future callers) without going through Parse. Translate
// MUST refuse a fault whose modifier does not belong to its verb rather than
// silently applying a nonsensical toxic (e.g. "jitter" tacked onto "down").
func TestTranslate_RejectsModifierNotOwnedByVerb(t *testing.T) {
	f := config.Fault{
		Name:   "bad",
		At:     "peak",
		Target: "redis:6379",
		Verb:   "down",
		Inject: map[string]any{"down": true, "jitter": "50ms"},
	}

	_, err := Translate(f)
	if err == nil {
		t.Fatal("Translate: want error for jitter modifier on down verb, got nil")
	}
}

// spec: R-CFG-23
// workers is the modifier this package owns (cpu/mem/io/fd). R-CFG-23
// requires it be an integer >= 1, checked independently of config.Parse
// (R-DC2-6 defense in depth: a Fault built directly, bypassing Parse, must
// still be caught here). workers: 0 is the dangerous case, not just an
// invalid one: a stress-ng run with zero workers does nothing, the fault
// silently never fires, and the run's "passed" verdict is a lie about what
// was actually tested. The error must name the fault, the modifier, and the
// legal range so a human editing torture.yaml can fix it without guessing.
func TestTranslate_RejectsWorkersBelowRange(t *testing.T) {
	cases := []int{0, -1}
	for _, workers := range cases {
		f := config.Fault{
			Name: "cpu_squeeze", Target: "checkout-api", Verb: "cpu",
			Inject: map[string]any{"cpu": "90%", "workers": workers},
		}
		_, err := Translate(f)
		if err == nil {
			t.Fatalf("Translate: workers=%d: want error, got nil", workers)
		}
		msg := err.Error()
		for _, want := range []string{"cpu_squeeze", "workers", "1"} {
			if !strings.Contains(msg, want) {
				t.Errorf("Translate: workers=%d: error %q does not mention %q (fault name/modifier/legal range required)", workers, msg, want)
			}
		}
	}
}

// spec: R-CFG-23
// workers: 1 is the boundary value the range is anchored on and MUST be
// legal — a range check that is off by one here would reject the smallest
// real stress-ng run.
func TestTranslate_AllowsWorkersAtLowerBoundary(t *testing.T) {
	f := config.Fault{
		Name: "cpu_squeeze", Target: "checkout-api", Verb: "cpu",
		Inject: map[string]any{"cpu": "90%", "workers": 1},
	}
	if _, err := Translate(f); err != nil {
		t.Fatalf("Translate: workers=1 must be legal, got error: %v", err)
	}
}

// spec: R-CFG-23
// The project's own reference config declares workers: 4 (torture.example.
// yaml's cpu_squeeze fault). Rejecting it would be the same class of defect
// R-EXE-15 called out for error_rate: a check that breaks the reference
// document it is supposed to validate.
func TestTranslate_ExampleConfigWorkersValueIsLegal(t *testing.T) {
	raw, err := os.ReadFile("../../torture.example.yaml")
	if err != nil {
		t.Fatalf("reading torture.example.yaml: %v", err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("config.Parse(torture.example.yaml): %v", err)
	}

	found := false
	for _, f := range cfg.Faults {
		if _, ok := f.Inject["workers"]; !ok {
			continue
		}
		found = true
		if _, err := Translate(f); err != nil {
			t.Errorf("Translate(%s): workers value from the reference config was rejected: %v", f.Name, err)
		}
	}
	if !found {
		t.Fatal("torture.example.yaml has no fault with a workers modifier; test can't prove anything")
	}
}

// spec: R-EXE-6
// Every service-scoped verb (cpu/mem/io/fd/cpu_limit/mem_limit/pause/kill/
// graceful) MUST translate to a DockerAction bound to the fault's own
// target container, never to the host. Translate has no host-scope escape
// hatch: DockerAction always carries a Container field sourced from
// Fault.Target, and this test locks that structural guarantee for every
// service verb in the table so a future verb added here cannot quietly
// regress to host scope.
func TestTranslate_ServiceVerbsAreScopedToTargetContainer(t *testing.T) {
	serviceFaults := []config.Fault{
		{Name: "a", Target: "checkout-api", Verb: "cpu", Inject: map[string]any{"cpu": "90%", "workers": 4}},
		{Name: "b", Target: "checkout-api", Verb: "mem", Inject: map[string]any{"mem": "80%"}},
		{Name: "c", Target: "checkout-api", Verb: "io", Inject: map[string]any{"io": "1"}},
		{Name: "d", Target: "checkout-api", Verb: "fd", Inject: map[string]any{"fd": "1000"}},
		{Name: "e", Target: "checkout-api", Verb: "cpu_limit", Inject: map[string]any{"cpu_limit": "0.5"}},
		{Name: "f", Target: "checkout-api", Verb: "mem_limit", Inject: map[string]any{"mem_limit": "256m"}},
		{Name: "g", Target: "checkout-api", Verb: "pause", Inject: map[string]any{"pause": true}},
		{Name: "h", Target: "checkout-api", Verb: "kill", Inject: map[string]any{"kill": true}},
		{Name: "i", Target: "checkout-api", Verb: "graceful", Inject: map[string]any{"graceful": true}},
	}

	for _, f := range serviceFaults {
		act, err := Translate(f)
		if err != nil {
			t.Fatalf("Translate(%s): unexpected error: %v", f.Name, err)
		}
		if act.Kind != KindDocker {
			t.Fatalf("Translate(%s): want KindDocker, got %v", f.Name, act.Kind)
		}
		if act.Docker == nil {
			t.Fatalf("Translate(%s): Docker action is nil", f.Name)
		}
		if act.Docker.Container != f.Target {
			t.Fatalf("Translate(%s): Container = %q, want %q (must never be host-scoped)", f.Name, act.Docker.Container, f.Target)
		}
		if act.Docker.Container == "" || act.Docker.Container == "host" {
			t.Fatalf("Translate(%s): Container = %q is not a valid container scope", f.Name, act.Docker.Container)
		}
	}
}

// spec: R-EXE-15
// error_rate is a real v0 verb (SPEC.md §4.4 table) but is owned by the mock
// provider (internal/egress, WireMock's fault mode — see
// torture.example.yaml's stripe_errors fault), not by Toxiproxy or Docker.
// R-EXE-15 requires a layer to pass over a verb it does not own rather than
// reject it: rejecting here would make torture.example.yaml itself
// unrunnable. This asserts the specific pass-over shape (Kind and Owner),
// not just "no error" — a change that silently dropped the Owner or routed
// it to the wrong layer must fail this test.
func TestTranslate_PassesOverVerbsOwnedByOtherLayers(t *testing.T) {
	cases := []struct {
		fault config.Fault
		owner string
	}{
		{
			fault: config.Fault{
				Name: "stripe_errors", Target: "api.stripe.com", Verb: "error_rate",
				Inject: map[string]any{"error_rate": 0.15, "status": 503},
			},
			owner: "internal/egress",
		},
		{
			fault: config.Fault{
				Name: "bad_message", Target: "orders_queue", Verb: "poison_pill",
				Inject: map[string]any{"poison_pill": true, "count": 3},
			},
			owner: "internal/queuefault",
		},
		{
			fault: config.Fault{
				Name: "dup_message", Target: "orders_queue", Verb: "duplicate",
				Inject: map[string]any{"duplicate": 0.1},
			},
			owner: "internal/queuefault",
		},
	}

	for _, c := range cases {
		act, err := Translate(c.fault)
		if err != nil {
			t.Fatalf("Translate(%s): want pass-over (nil error), got error: %v", c.fault.Name, err)
		}
		if act.Kind != KindPassed {
			t.Fatalf("Translate(%s): Kind = %v, want KindPassed", c.fault.Name, act.Kind)
		}
		if act.Owner != c.owner {
			t.Fatalf("Translate(%s): Owner = %q, want %q", c.fault.Name, act.Owner, c.owner)
		}
		if act.Toxic != nil || act.Docker != nil {
			t.Fatalf("Translate(%s): a passed-over fault must carry no Toxic/Docker payload", c.fault.Name)
		}
	}
}

// spec: R-EXE-15
// The reference config itself is the acceptance test the ruling names:
// torture.example.yaml declares error_rate (owned by internal/egress) among
// faults this package does own, and check.py certifies that file as valid.
// A layer that errors on an unowned verb makes the project's own reference
// document unrunnable — every fault in it MUST translate without error.
func TestTranslate_ExampleConfigFaultsAllTranslateWithoutError(t *testing.T) {
	raw, err := os.ReadFile("../../torture.example.yaml")
	if err != nil {
		t.Fatalf("reading torture.example.yaml: %v", err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("config.Parse(torture.example.yaml): %v", err)
	}
	if len(cfg.Faults) == 0 {
		t.Fatal("torture.example.yaml declares no faults; test can't prove anything")
	}

	for _, f := range cfg.Faults {
		if _, err := Translate(f); err != nil {
			t.Errorf("Translate(%s): %v", f.Name, err)
		}
	}
}
