package fault

import (
	"fmt"

	"github.com/jdb316/tortureu/internal/config"
)

// ActionKind distinguishes the two translation targets this package
// produces (PLAN.md Task 5): a Toxiproxy toxic (network target) or a Docker
// action scoped to the target container (service target, R-EXE-6).
type ActionKind string

const (
	KindToxic  ActionKind = "toxic"
	KindDocker ActionKind = "docker"
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

// Action is one translated fault.
type Action struct {
	Fault  config.Fault
	Kind   ActionKind
	Toxic  *Toxic
	Docker *DockerAction
}

// networkVerbs applies to a network target and translates to a Toxiproxy
// toxic. serviceVerbs applies to a service/container and translates to a
// Docker action. Table is SPEC.md §4.4 / R-CFG-14, minus the verbs this
// package does not translate (see unsupportedVerbs).
var networkVerbs = map[string]bool{
	"latency":   true,
	"down":      true,
	"bandwidth": true,
	"slicer":    true,
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
}

// unsupportedVerbs are real R-CFG-14 verbs this package does not translate:
// error_rate targets a mocked host (WireMock's fault mode, per
// torture.example.yaml), and poison_pill/duplicate target a queue. Neither
// is a Toxiproxy toxic nor a Docker action, which is the whole of this
// package's scope per PLAN.md Task 5. See task-5-report.md for the gap.
var unsupportedVerbs = map[string]string{
	"error_rate":  "targets a mocked host (WireMock), not Toxiproxy or Docker",
	"poison_pill": "targets a queue, not Toxiproxy or Docker",
	"duplicate":   "targets a queue, not Toxiproxy or Docker",
}

// Translate converts one parsed fault into a Toxiproxy toxic or a Docker
// action (PLAN.md Task 5). It re-validates the verb/modifier pairing
// (R-CFG-14) rather than trusting the caller went through config.Parse.
func Translate(f config.Fault) (Action, error) {
	allowed, known := faultVerbModifiers[f.Verb]
	if !known {
		return Action{}, fmt.Errorf("fault %q: unknown inject verb %q", f.Name, f.Verb)
	}
	for k := range f.Inject {
		if k == f.Verb {
			continue
		}
		if !allowed[k] {
			return Action{}, fmt.Errorf("fault %q: modifier %q is not valid for verb %q", f.Name, k, f.Verb)
		}
	}

	if reason, unsupported := unsupportedVerbs[f.Verb]; unsupported {
		return Action{}, fmt.Errorf("fault %q: verb %q is not translated by internal/fault: %s", f.Name, f.Verb, reason)
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
		return Action{}, fmt.Errorf("fault %q: verb %q has no translation", f.Name, f.Verb)
	}
}

func translateToxic(f config.Fault) (Action, error) {
	t := &Toxic{Attributes: map[string]any{}}
	switch f.Verb {
	case "latency":
		t.Type = "latency"
		t.Attributes["latency"] = f.Inject["latency"]
		if jitter, ok := f.Inject["jitter"]; ok {
			t.Attributes["jitter"] = jitter
		}
	case "down":
		t.Disable = true
	case "bandwidth":
		t.Type = "bandwidth"
		t.Attributes["rate"] = f.Inject["bandwidth"]
	case "slicer":
		t.Type = "slicer"
		t.Attributes["average_size"] = f.Inject["slicer"]
		if delay, ok := f.Inject["delay"]; ok {
			t.Attributes["delay"] = delay
		}
	}
	return Action{Fault: f, Kind: KindToxic, Toxic: t}, nil
}

func translateDocker(f config.Fault) (Action, error) {
	d := &DockerAction{Container: f.Target, Args: map[string]any{}}
	switch f.Verb {
	case "cpu", "mem", "io", "fd":
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
	case "kill":
		d.Kind = "kill"
	case "graceful":
		d.Kind = "graceful"
	}
	return Action{Fault: f, Kind: KindDocker, Docker: d}, nil
}
