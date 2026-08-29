package fault

import (
	"os"
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/config"
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

// spec: R-CFG-14
// R-CFG-14's table promises latency injects "added delay" and bandwidth
// injects a "rate cap" via Toxiproxy — a promise this package breaks if it
// passes torture.yaml's documented unit-suffixed syntax straight through as
// a string into Toxiproxy's numeric fields. B1 measured exactly this:
// `latency: 300ms` produces an HTTP 400 from Toxiproxy and the fault never
// applies, even though torture.example.yaml (certified by check.py) uses
// this exact syntax. Toxiproxy's latency toxic takes "latency"/"jitter" in
// milliseconds; this asserts the ms suffix is parsed to the correct integer
// millisecond value, not passed through as "300ms".
func TestTranslate_ParsesLatencyAndJitterMillisecondSuffix(t *testing.T) {
	f := config.Fault{
		Name: "pg_slow", Target: "postgres:5432", Verb: "latency",
		Inject: map[string]any{"latency": "300ms", "jitter": "50ms"},
	}

	act, err := Translate(f)
	if err != nil {
		t.Fatalf("Translate: unexpected error: %v", err)
	}
	if got := act.Toxic.Attributes["latency"]; got != 300 {
		t.Errorf("Attributes[latency] = %v (%T), want int 300", got, got)
	}
	if got := act.Toxic.Attributes["jitter"]; got != 50 {
		t.Errorf("Attributes[jitter] = %v (%T), want int 50", got, got)
	}
}

// spec: R-CFG-14
// Same defect class for bandwidth: Toxiproxy's bandwidth toxic "rate"
// field is KB/s. `1mbps` (1 megabit/s) must convert to 125 KB/s
// (1,000,000 bits/s ÷ 8 ÷ 1000), not be passed through as the string
// "1mbps" — B1's measured 400 for this exact case.
func TestTranslate_ParsesBandwidthRateSuffix(t *testing.T) {
	f := config.Fault{
		Name: "wan_cap", Target: "postgres:5432", Verb: "bandwidth",
		Inject: map[string]any{"bandwidth": "1mbps"},
	}

	act, err := Translate(f)
	if err != nil {
		t.Fatalf("Translate: unexpected error: %v", err)
	}
	if got := act.Toxic.Attributes["rate"]; got != 125 {
		t.Errorf("Attributes[rate] = %v (%T), want int 125 (KB/s)", got, got)
	}
}

// spec: R-CFG-14
// B1 confirmed bare numeric input already works against the live
// Toxiproxy instrument ("re-ran with numeric input, bypassing the gap:
// latency PASS, bandwidth PASS"). The unit-suffix fix must not regress
// that: a caller passing an already-correct integer must see it pass
// through unchanged, not be reinterpreted or rejected.
func TestTranslate_AcceptsBareNumericLatencyAndBandwidth(t *testing.T) {
	lat, err := Translate(config.Fault{
		Name: "a", Target: "postgres:5432", Verb: "latency",
		Inject: map[string]any{"latency": 300},
	})
	if err != nil {
		t.Fatalf("Translate(latency=300): unexpected error: %v", err)
	}
	if got := lat.Toxic.Attributes["latency"]; got != 300 {
		t.Errorf("Attributes[latency] = %v, want 300", got)
	}

	bw, err := Translate(config.Fault{
		Name: "b", Target: "postgres:5432", Verb: "bandwidth",
		Inject: map[string]any{"bandwidth": 500},
	})
	if err != nil {
		t.Fatalf("Translate(bandwidth=500): unexpected error: %v", err)
	}
	if got := bw.Toxic.Attributes["rate"]; got != 500 {
		t.Errorf("Attributes[rate] = %v, want 500", got)
	}
}

// spec: R-CFG-14
// A malformed unit MUST error clearly rather than be silently coerced
// (e.g. truncated to its leading digits) — a silently-wrong latency value
// is worse than a rejected one, for the same reason R-CFG-23 rejects
// workers: 0 rather than guessing. The error must name the fault, the
// field, and the accepted forms.
func TestTranslate_RejectsMalformedDurationUnit(t *testing.T) {
	f := config.Fault{
		Name: "pg_slow", Target: "postgres:5432", Verb: "latency",
		Inject: map[string]any{"latency": "300bogus"},
	}
	_, err := Translate(f)
	if err == nil {
		t.Fatal("Translate: want error for malformed latency unit, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"pg_slow", "latency", "300bogus"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q (fault/field/value required)", msg, want)
		}
	}
}

// spec: R-CFG-14
// Same requirement for bandwidth's rate suffix.
func TestTranslate_RejectsMalformedRateUnit(t *testing.T) {
	f := config.Fault{
		Name: "wan_cap", Target: "postgres:5432", Verb: "bandwidth",
		Inject: map[string]any{"bandwidth": "1gbps-ish"},
	}
	_, err := Translate(f)
	if err == nil {
		t.Fatal("Translate: want error for malformed bandwidth unit, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"wan_cap", "bandwidth", "1gbps-ish"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q (fault/field/value required)", msg, want)
		}
	}
}

// spec: R-CFG-14
// torture.example.yaml's own pg_slow fault (latency: 300ms, jitter: 50ms)
// is the exact syntax B1 found broken. This nails down the fix against the
// project's own reference document, not just synthetic cases.
func TestTranslate_ExampleConfigLatencyConvertsToMilliseconds(t *testing.T) {
	raw, err := os.ReadFile("../../torture.example.yaml")
	if err != nil {
		t.Fatalf("reading torture.example.yaml: %v", err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("config.Parse(torture.example.yaml): %v", err)
	}

	var found bool
	for _, f := range cfg.Faults {
		if f.Name != "pg_slow" {
			continue
		}
		found = true
		act, err := Translate(f)
		if err != nil {
			t.Fatalf("Translate(pg_slow): unexpected error: %v", err)
		}
		if got := act.Toxic.Attributes["latency"]; got != 300 {
			t.Errorf("Attributes[latency] = %v, want int 300", got)
		}
		if got := act.Toxic.Attributes["jitter"]; got != 50 {
			t.Errorf("Attributes[jitter] = %v, want int 50", got)
		}
	}
	if !found {
		t.Fatal("torture.example.yaml has no pg_slow fault; test can't prove anything")
	}
}

// spec: R-EXE-6
// B1 measured `cpu: 90%` producing ~403-416% of one core through the
// internal/run applier, because translateDocker put the raw string "90%"
// into a generic "amount" key the applier had no unambiguous way to
// interpret. The fix on this side of the boundary: emit the percentage as
// an int under a dedicated "cpu_percent" key (0-100, no "%" sign, no
// sharing a key name with mem/io/fd's differently-shaped "amount"), so the
// applier the other agent is fixing has exactly one way to read it. This
// also doubles as an R-CFG-14-style "don't silently accept nonsense" guard:
// a percentage outside 0-100 is rejected rather than passed through to
// become another miscalibrated stress-ng run.
func TestTranslate_CPUPercentEmittedUnambiguously(t *testing.T) {
	f := config.Fault{
		Name: "cpu_squeeze", Target: "checkout-api", Verb: "cpu",
		Inject: map[string]any{"cpu": "90%", "workers": 4},
	}
	act, err := Translate(f)
	if err != nil {
		t.Fatalf("Translate: unexpected error: %v", err)
	}
	pct, ok := act.Docker.Args["cpu_percent"]
	if !ok {
		t.Fatal(`Docker.Args["cpu_percent"] missing`)
	}
	if pct != 90 {
		t.Fatalf(`Docker.Args["cpu_percent"] = %v (%T), want int 90`, pct, pct)
	}
	if _, stillPresent := act.Docker.Args["amount"]; stillPresent {
		t.Fatal(`Docker.Args["amount"] must not also be set for cpu — exactly one unambiguous key`)
	}
}

// spec: R-EXE-6
// A percentage outside 0-100 (e.g. a "500%" typo — the same failure class
// as R-CFG-23's duplicate: 5) must be rejected, not passed through to
// mis-drive the stressor the way the untyped "90%" string did.
func TestTranslate_RejectsCPUPercentOutOfRange(t *testing.T) {
	f := config.Fault{
		Name: "cpu_squeeze", Target: "checkout-api", Verb: "cpu",
		Inject: map[string]any{"cpu": "500%"},
	}
	_, err := Translate(f)
	if err == nil {
		t.Fatal("Translate: want error for cpu: 500%, got nil")
	}
}

// spec: R-EXE-15
// B1 measured `kill` producing a graceful close (EOF) instead of an RST —
// indistinguishable from `graceful` to the client. R-CFG-15/R-EXE-15 and
// the registry's "signals" entry ("SIGSTOP vs SIGKILL vs SIGTERM = 3
// failure classes") require these to stay distinct. Investigation: this
// package already emitted distinct DockerAction.Kind values ("kill" vs
// "graceful") before this fix, so the collapse is not happening here — it
// is the internal/run applier (owned by another agent) not sending
// different signals for the two Kinds. This closes the ambiguity on this
// package's side of the boundary the same way cpu_percent did: emit the
// exact signal name so the applier has no interpretation left to get
// wrong. pause maps to SIGSTOP for the same reason, even though Docker's
// native `docker pause` uses the cgroup freezer rather than a literal
// SIGSTOP delivery — the client-visible effect (process stops responding
// without closing sockets) is SIGSTOP's, and that's what callers need to
// reason about.
func TestTranslate_KillPauseGracefulEmitDistinctSignals(t *testing.T) {
	cases := []struct {
		verb, wantSignal string
	}{
		{"kill", "SIGKILL"},
		{"graceful", "SIGTERM"},
		{"pause", "SIGSTOP"},
	}
	for _, c := range cases {
		act, err := Translate(config.Fault{
			Name: "svc_" + c.verb, Target: "checkout-api", Verb: c.verb,
			Inject: map[string]any{c.verb: true},
		})
		if err != nil {
			t.Fatalf("Translate(%s): unexpected error: %v", c.verb, err)
		}
		got, ok := act.Docker.Args["signal"]
		if !ok {
			t.Fatalf("Translate(%s): Docker.Args[\"signal\"] missing", c.verb)
		}
		if got != c.wantSignal {
			t.Fatalf("Translate(%s): signal = %v, want %q", c.verb, got, c.wantSignal)
		}
	}

	// Same fault verbs must never resolve to the same signal — that is
	// exactly the collapse B1 measured.
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.wantSignal] {
			t.Fatalf("signal %q reused across verbs — the three classes must stay distinct", c.wantSignal)
		}
		seen[c.wantSignal] = true
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
