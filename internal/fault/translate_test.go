// Package fault translates config.Fault into Toxiproxy toxics and Docker
// actions (SPEC.md §4.4, §5).
package fault

import (
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

// spec: R-CFG-14
// error_rate is a real v0 verb (SPEC.md §4.4 table) but targets a mocked
// host via WireMock's fault mode (see torture.example.yaml's stripe_errors
// fault) — it is neither a Toxiproxy toxic nor a Docker action, which is
// this package's whole scope per PLAN.md Task 5. Translate MUST reject it
// with a clear error rather than silently dropping the fault or mapping it
// to the wrong action kind.
func TestTranslate_RejectsVerbOutsideToxiproxyAndDockerScope(t *testing.T) {
	f := config.Fault{
		Name:   "stripe_errors",
		Target: "api.stripe.com",
		Verb:   "error_rate",
		Inject: map[string]any{"error_rate": 0.15, "status": 503},
	}

	_, err := Translate(f)
	if err == nil {
		t.Fatal("Translate: want error for error_rate (WireMock-only verb), got nil")
	}
}
