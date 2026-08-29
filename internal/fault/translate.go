// Package fault translates config.Fault into Toxiproxy toxics and Docker
// actions (SPEC.md §4.4, §5), and tears every applied fault down on exit
// (R-EXE-5, R-EXE-16).
//
// Teardown limit (R-EXE-16): Manager's teardown runs on an in-process
// panic (a deferred Teardown/recover) and on SIGINT/SIGTERM (WatchSignals).
// SIGKILL cannot be caught by any process — the OS terminates immediately,
// so no Go code in this package or anywhere else runs, and a SIGKILL'd run
// WILL leave faults applied. This package does not claim otherwise. It does
// not yet make faults recoverable on next start (R-EXE-16's SHOULD); a
// SIGKILL'd run's faults must currently be removed manually.
package fault

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/jd316/TortureU/internal/config"
)

// ActionKind distinguishes the two translation targets this package
// produces (PLAN.md Task 5): a Toxiproxy toxic (network target) or a Docker
// action scoped to the target container (service target, R-EXE-6).
type ActionKind string

const (
	KindToxic  ActionKind = "toxic"
	KindDocker ActionKind = "docker"
	// KindPassed marks a verb this package does not own (R-EXE-15): the
	// fault is well-formed, but ownership belongs to another layer (the
	// mock provider or the queue-fault broker producer). Toxic and Docker
	// are both nil; Owner names the layer the caller should route to.
	KindPassed ActionKind = "passed"
)

// Toxic is a Toxiproxy toxic ready to be applied to the proxy for a
// network target. Attributes uses Toxiproxy's own attribute names for the
// underlying toxic type (latency/jitter in ms, rate in KB/s, etc.) — that
// schema is Toxiproxy's public API, not something SPEC.md defines.
type Toxic struct {
	// Type is the Toxiproxy toxic type ("latency", "bandwidth", "slicer"),
	// or "" when Disable is set (down: connection refused via a disabled
	// proxy rather than a toxic).
	Type       string
	Disable    bool
	Attributes map[string]any
}

// DockerAction is a Docker-scoped action against exactly one named
// container (R-EXE-6: container scope only, never the host).
type DockerAction struct {
	Kind      string // "stress", "cpu_limit", "mem_limit", "pause", "kill", "graceful"
	Container string
	Args      map[string]any
}

// Action is one translated fault. When Kind is KindPassed, Toxic and Docker
// are both nil and Owner names the layer that owns the verb instead.
type Action struct {
	Fault  config.Fault
	Kind   ActionKind
	Toxic  *Toxic
	Docker *DockerAction
	Owner  string
}

// networkVerbs applies to a network target and translates to a Toxiproxy
// toxic. serviceVerbs applies to a service/container and translates to a
// Docker action. Table is SPEC.md §4.4 / R-CFG-14 plus the Toxiproxy-only
// verbs R-EXE-15 adds (timeout, reset_peer).
var networkVerbs = map[string]bool{
	"latency":    true,
	"down":       true,
	"bandwidth":  true,
	"slicer":     true,
	"timeout":    true,
	"reset_peer": true,
}

var serviceVerbs = map[string]bool{
	"cpu":       true,
	"mem":       true,
	"io":        true,
	"fd":        true,
	"cpu_limit": true,
	"mem_limit": true,
	"pause":     true,
	"kill":      true,
	"graceful":  true,
}

// faultVerbModifiers mirrors the SPEC.md §4.4 / R-CFG-14 table: the
// modifiers each verb owns. Duplicated from internal/config (which cannot
// be imported for this — its table is unexported) so Translate can defend
// against a Fault built directly rather than via config.Parse.
var faultVerbModifiers = map[string]map[string]bool{
	"latency":     {"jitter": true},
	"down":        {},
	"bandwidth":   {},
	"slicer":      {"delay": true},
	"error_rate":  {"status": true},
	"cpu":         {"workers": true},
	"mem":         {"workers": true},
	"io":          {"workers": true},
	"fd":          {"workers": true},
	"cpu_limit":   {},
	"mem_limit":   {},
	"pause":       {},
	"kill":        {},
	"graceful":    {},
	"poison_pill": {"count": true},
	"duplicate":   {},
	"timeout":     {},
	"reset_peer":  {},
}

// otherLayerOwner is R-EXE-15's ownership table for verbs this package does
// not translate itself: error_rate is legal only on a class: mock host and
// is applied by the mock provider (WireMock); poison_pill/duplicate target
// a queue and are applied by the broker producer. Translate MUST pass these
// over rather than reject them — torture.example.yaml declares error_rate,
// so rejecting it would make the project's own reference config unrunnable.
var otherLayerOwner = map[string]string{
	"error_rate":  "internal/egress",
	"poison_pill": "internal/queuefault",
	"duplicate":   "internal/queuefault",
}

// Translate converts one parsed fault into a Toxiproxy toxic or a Docker
// action (PLAN.md Task 5), or passes it over (R-EXE-15) when it belongs to
// a layer this package does not implement. It re-validates the
// verb/modifier pairing (R-CFG-14) rather than trusting the caller went
// through config.Parse. An error means the verb is not recognized by *any*
// layer's table — that is the only case R-EXE-15 says a layer should reject.
func Translate(f config.Fault) (Action, error) {
	allowed, known := faultVerbModifiers[f.Verb]
	if !known {
		return Action{}, fmt.Errorf("fault %q: %q is not a recognized inject verb", f.Name, f.Verb)
	}
	for k := range f.Inject {
		if k == f.Verb {
			continue
		}
		if !allowed[k] {
			return Action{}, fmt.Errorf("fault %q: %q is not a valid modifier for verb %q", f.Name, k, f.Verb)
		}
	}

	if owner, ownedElsewhere := otherLayerOwner[f.Verb]; ownedElsewhere {
		return Action{Fault: f, Kind: KindPassed, Owner: owner}, nil
	}

	// R-CFG-23: workers is the numeric modifier this package owns (cpu/mem/
	// io/fd). Re-checked here independently of config.Parse (R-DC2-6
	// defense in depth) because a Fault can be built without going through
	// Parse. workers: 0 is the dangerous case, not just invalid: a
	// stress-ng run with zero workers silently does nothing, so the fault
	// never fires and the run's verdict lies about what was tested.
	if workers, ok := f.Inject["workers"]; ok {
		if err := validateWorkers(f.Name, workers); err != nil {
			return Action{}, err
		}
	}

	if f.Target == "" {
		return Action{}, fmt.Errorf("fault %q: target is required", f.Name)
	}

	switch {
	case networkVerbs[f.Verb]:
		return translateToxic(f)
	case serviceVerbs[f.Verb]:
		return translateDocker(f)
	default:
		return Action{}, fmt.Errorf("fault %q: %q is not a supported inject verb", f.Name, f.Verb)
	}
}

// validateWorkers enforces R-CFG-23: workers MUST be an integer >= 1. The
// error names the fault, the modifier, and the legal range, per R-CFG-23's
// own requirement for the error shape.
func validateWorkers(faultName string, v any) error {
	n, isInt := asInt(v)
	if !isInt || n < 1 {
		return fmt.Errorf("fault %q: modifier \"workers\" = %v is invalid; must be an integer >= 1", faultName, v)
	}
	return nil
}

// asInt reports whether v is a whole number, accepting both the int Go's
// yaml decoder produces for bare integers and the float64 it can produce
// for numbers parsed generically.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n != math.Trunc(n) {
			return 0, false
		}
		return int(n), true
	default:
		return 0, false
	}
}

// durationPattern accepts a bare number (assumed already in milliseconds —
// the form B1 confirmed already works against live Toxiproxy), a number
// with an "ms" suffix (torture.example.yaml's pg_slow fault, "300ms"), or a
// number with a plain "s" suffix (torture.example.yaml's stripe_slow
// fault, "2s") — both real syntaxes B1 found silently passed through as a
// string instead of being converted.
var durationPattern = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)\s*(ms|s)?$`)

// parseMillis converts a latency/jitter/timeout field to the integer
// milliseconds Toxiproxy's latency, timeout, and reset_peer toxics expect.
// A malformed value errors naming the fault, the field, and the value
// given, rather than silently truncating or coercing it (R-CFG-14: a
// wrong-but-accepted latency value produces a run whose fault silently
// never fired, same failure class as R-CFG-23's workers: 0).
func parseMillis(faultName, field string, v any) (int, error) {
	if n, ok := asInt(v); ok {
		return n, nil
	}
	if f, ok := v.(float64); ok {
		return int(math.Round(f)), nil
	}
	invalid := fmt.Errorf("fault %q: %s = %v is invalid; accepted forms: a bare number of milliseconds, or a number with an \"ms\" or \"s\" suffix (e.g. \"300ms\", \"2s\")", faultName, field, v)
	s, ok := v.(string)
	if !ok {
		return 0, invalid
	}
	m := durationPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("fault %q: %s = %q is invalid; accepted forms: a bare number of milliseconds, or a number with an \"ms\" or \"s\" suffix (e.g. \"300ms\", \"2s\")", faultName, field, s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("fault %q: %s = %q is invalid; accepted forms: a bare number of milliseconds, or a number with an \"ms\" or \"s\" suffix (e.g. \"300ms\", \"2s\")", faultName, field, s)
	}
	if strings.EqualFold(m[2], "s") {
		n *= 1000
	}
	return int(math.Round(n)), nil
}

// ratePattern accepts a bare number (assumed already in KB/s — the form B1
// confirmed already works) or a number with an "mbps" (megabits/s) or
// "kbps" (kilobits/s) suffix, torture.yaml's documented bandwidth syntax
// (e.g. "1mbps").
var ratePattern = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)\s*(mbps|kbps)$`)

// parseRateKBps converts a bandwidth field to the integer KB/s Toxiproxy's
// bandwidth toxic "rate" attribute expects. Same "error, don't coerce"
// rule as parseMillis.
func parseRateKBps(faultName, field string, v any) (int, error) {
	if n, ok := asInt(v); ok {
		return n, nil
	}
	if f, ok := v.(float64); ok {
		return int(math.Round(f)), nil
	}
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("fault %q: %s = %v is invalid; accepted forms: a bare number of KB/s, or a number with an \"mbps\"/\"kbps\" suffix (e.g. \"1mbps\")", faultName, field, v)
	}
	m := ratePattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("fault %q: %s = %q is invalid; accepted forms: a bare number of KB/s, or a number with an \"mbps\"/\"kbps\" suffix (e.g. \"1mbps\")", faultName, field, s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("fault %q: %s = %q is invalid; accepted forms: a bare number of KB/s, or a number with an \"mbps\"/\"kbps\" suffix (e.g. \"1mbps\")", faultName, field, s)
	}
	// bits/s -> bytes/s (÷8) -> KB/s (÷1000, decimal kilo).
	switch strings.ToLower(m[2]) {
	case "mbps":
		n = n * 1_000_000 / 8 / 1000
	case "kbps":
		n = n * 1_000 / 8 / 1000
	}
	return int(math.Round(n)), nil
}

func translateToxic(f config.Fault) (Action, error) {
	t := &Toxic{Attributes: map[string]any{}}
	switch f.Verb {
	case "latency":
		t.Type = "latency"
		latency, err := parseMillis(f.Name, "latency", f.Inject["latency"])
		if err != nil {
			return Action{}, err
		}
		t.Attributes["latency"] = latency
		if jitter, ok := f.Inject["jitter"]; ok {
			jitterMS, err := parseMillis(f.Name, "jitter", jitter)
			if err != nil {
				return Action{}, err
			}
			t.Attributes["jitter"] = jitterMS
		}
	case "down":
		t.Disable = true
	case "bandwidth":
		t.Type = "bandwidth"
		rate, err := parseRateKBps(f.Name, "bandwidth", f.Inject["bandwidth"])
		if err != nil {
			return Action{}, err
		}
		t.Attributes["rate"] = rate
	case "slicer":
		t.Type = "slicer"
		t.Attributes["average_size"] = f.Inject["slicer"]
		if delay, ok := f.Inject["delay"]; ok {
			t.Attributes["delay"] = delay
		}
	case "timeout":
		t.Type = "timeout"
		t.Attributes["timeout"] = f.Inject["timeout"]
	case "reset_peer":
		t.Type = "reset_peer"
		t.Attributes["timeout"] = f.Inject["reset_peer"]
	}
	return Action{Fault: f, Kind: KindToxic, Toxic: t}, nil
}

// percentPattern accepts a bare number or a number with a trailing "%"
// (torture.yaml's documented cpu syntax, e.g. "90%").
var percentPattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*%?$`)

// parsePercent converts a cpu load field to an integer percent in [0, 100].
// B1 measured cpu: 90% producing ~403-416% of one core because the
// untyped "90%" string reached the Docker applier under a generic key with
// no defined unit; this both parses the value unambiguously and rejects
// anything outside a real percentage (the same "reject nonsense, don't
// coerce it" rule as R-CFG-23's workers >= 1).
func parsePercent(faultName, field string, v any) (int, error) {
	invalid := fmt.Errorf("fault %q: %s = %v is invalid; must be a percentage from 0 to 100 (e.g. \"90%%\" or 90)", faultName, field, v)
	var n float64
	switch val := v.(type) {
	case int:
		n = float64(val)
	case int64:
		n = float64(val)
	case float64:
		n = val
	case string:
		m := percentPattern.FindStringSubmatch(val)
		if m == nil {
			return 0, invalid
		}
		parsed, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, invalid
		}
		n = parsed
	default:
		return 0, invalid
	}
	if n < 0 || n > 100 {
		return 0, invalid
	}
	return int(math.Round(n)), nil
}

func translateDocker(f config.Fault) (Action, error) {
	d := &DockerAction{Container: f.Target, Args: map[string]any{}}
	switch f.Verb {
	case "cpu":
		d.Kind = "stress"
		d.Args["resource"] = "cpu"
		pct, err := parsePercent(f.Name, "cpu", f.Inject["cpu"])
		if err != nil {
			return Action{}, err
		}
		// cpu_percent: integer 0-100, the target CPU load per stress-ng
		// worker (stress-ng's --cpu-load semantics). Deliberately a
		// distinct key from "amount" (used by mem/io/fd below) so the
		// Docker applier has exactly one, unit-typed way to read it.
		d.Args["cpu_percent"] = pct
		if workers, ok := f.Inject["workers"]; ok {
			d.Args["workers"] = workers
		}
	case "mem", "io", "fd":
		d.Kind = "stress"
		d.Args["resource"] = f.Verb
		d.Args["amount"] = f.Inject[f.Verb]
		if workers, ok := f.Inject["workers"]; ok {
			d.Args["workers"] = workers
		}
	case "cpu_limit":
		d.Kind = "cpu_limit"
		d.Args["limit"] = f.Inject["cpu_limit"]
	case "mem_limit":
		d.Kind = "mem_limit"
		d.Args["limit"] = f.Inject["mem_limit"]
	case "pause":
		d.Kind = "pause"
		// SIGSTOP-equivalent: Docker's native `docker pause` uses the
		// cgroup freezer rather than literally delivering SIGSTOP, but the
		// client-visible effect matches SIGSTOP's (process stops
		// responding without closing sockets), which is what the R-CFG-15
		// three-class distinction is about.
		d.Args["signal"] = "SIGSTOP"
	case "kill":
		d.Kind = "kill"
		d.Args["signal"] = "SIGKILL"
	case "graceful":
		d.Kind = "graceful"
		d.Args["signal"] = "SIGTERM"
	}
	return Action{Fault: f, Kind: KindDocker, Docker: d}, nil
}
