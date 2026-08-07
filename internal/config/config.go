// Package config parses and validates torture.yaml (SPEC.md §4).
package config

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Target describes the system under test (R-CFG-3).
type Target struct {
	Compose string `yaml:"compose"`
	Service string `yaml:"service"`
	BaseURL string `yaml:"base_url"`
	OpenAPI string `yaml:"openapi"`
}

// EgressHost classifies one external host (R-CFG-4, R-CFG-5).
type EgressHost struct {
	Class  string `yaml:"class"`
	From   string `yaml:"from"`
	MaxRPS int    `yaml:"max_rps"`
}

// Egress is the default-deny egress policy (DC-2, R-CFG-4).
type Egress struct {
	Default string                `yaml:"default"`
	Hosts   map[string]EgressHost `yaml:"hosts"`
}

// FlowStep is one request in a scenario flow (R-CFG-9).
type FlowStep struct {
	Method  string `yaml:"method"`
	Path    string `yaml:"path"`
	Body    string `yaml:"body"`
	Capture string `yaml:"capture"`
}

// Scenario is a weighted user journey (R-CFG-9).
type Scenario struct {
	Name   string     `yaml:"name"`
	Weight int        `yaml:"weight"`
	Flow   []FlowStep `yaml:"flow"`
}

// Stage is one segment of the load profile; its Phase is the anchor faults
// attach to (R-CFG-7, R-CFG-8).
type Stage struct {
	Phase string `yaml:"phase"`
	To    string `yaml:"to"`
	Hold  string `yaml:"hold"`
	Over  string `yaml:"over"`
	For   string `yaml:"for"`
}

// Load is the open-model (arrival_rate) load profile (R-CFG-6..9).
type Load struct {
	Engine    string     `yaml:"engine"`
	Model     string     `yaml:"model"`
	Stages    []Stage    `yaml:"stages"`
	Scenarios []Scenario `yaml:"scenarios"`
}

// rawScenario decodes flow entries as raw nodes so bare strings (R-CFG-9)
// can be rejected with a clear error instead of a generic yaml type error.
type rawScenario struct {
	Name   string      `yaml:"name"`
	Weight int         `yaml:"weight"`
	Flow   []yaml.Node `yaml:"flow"`
}

type rawLoad struct {
	Engine    string        `yaml:"engine"`
	Model     string        `yaml:"model"`
	Stages    []Stage       `yaml:"stages"`
	Scenarios []rawScenario `yaml:"scenarios"`
}

// faultVerbModifiers are the v0 inject verbs and the modifiers each one
// owns, per the SPEC.md §4.4 table (R-CFG-14). An empty set means the verb
// takes no modifiers.
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

// atGrammar matches R-CFG-11: <phase>, <phase>+<duration>, or t=<duration>.
var atGrammar = regexp.MustCompile(`^(t=[0-9]+[a-z]+|[A-Za-z0-9_-]+(\+[0-9]+[a-z]+)?)$`)

// asFloat extracts a numeric value decoded by yaml.v3 (int or float64).
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

// checkFraction enforces R-CFG-23: duplicate and error_rate are proportions
// in 0.0..1.0, not multipliers — the "duplicate: 5" (500%) motivating case.
func checkFraction(faultName, modifier string, v any) error {
	f, ok := asFloat(v)
	if !ok || f < 0.0 || f > 1.0 {
		return fmt.Errorf("torture.yaml: faults[%q]: inject: %s: %v is out of range, must be 0.0..1.0", faultName, modifier, v)
	}
	return nil
}

// checkPositiveInt enforces R-CFG-23: count and workers are integers >= 1.
func checkPositiveInt(faultName, modifier string, v any) error {
	f, ok := asFloat(v)
	if !ok || f != float64(int(f)) || int(f) < 1 {
		return fmt.Errorf("torture.yaml: faults[%q]: inject: %s: %v is out of range, must be an integer >= 1", faultName, modifier, v)
	}
	return nil
}

// Fault is one injected fault, anchored to a load phase (R-CFG-10..15).
type Fault struct {
	Name   string
	At     string
	For    string
	Target string
	Verb   string
	Inject map[string]any
}

// rawFault decodes a fault before the at:/inject: grammar is validated.
type rawFault struct {
	Name   string         `yaml:"name"`
	At     string         `yaml:"at"`
	For    string         `yaml:"for"`
	Target string         `yaml:"target"`
	Inject map[string]any `yaml:"inject"`
}

// defaultResetCommand is used when reset.command is absent (R-CFG-21).
const defaultResetCommand = "docker compose down -v && docker compose up -d --wait"

// Reset runs before each run by default (R-CFG-20); the command is
// user-supplied, defaulting to defaultResetCommand (R-CFG-21).
type Reset struct {
	Command string `yaml:"command"`
	Seed    string `yaml:"seed"`
}

// AssertEntry is one assertion: a k6 threshold {metric: [expr...]} verbatim
// (R-CFG-16), or a promql: (R-CFG-17) / sql: (R-CFG-18) escape hatch.
type AssertEntry map[string]any

// Config is the parsed and validated torture.yaml.
type Config struct {
	Version int           `yaml:"version"`
	Target  Target        `yaml:"target"`
	Egress  Egress        `yaml:"egress"`
	Load    Load          `yaml:"load"`
	Faults  []Fault       `yaml:"faults"`
	Assert  []AssertEntry `yaml:"assert"`
	Reset   Reset         `yaml:"reset"`
}

// rawConfig mirrors Config for decoding before validation runs.
type rawConfig struct {
	Version int           `yaml:"version"`
	Target  Target        `yaml:"target"`
	Egress  Egress        `yaml:"egress"`
	Load    rawLoad       `yaml:"load"`
	Faults  []rawFault    `yaml:"faults"`
	Assert  []AssertEntry `yaml:"assert"`
	Reset   Reset         `yaml:"reset"`
}

// topLevelKeys are the blocks R-CFG-2 allows at the top of torture.yaml.
var topLevelKeys = map[string]bool{
	"version": true,
	"target":  true,
	"egress":  true,
	"reset":   true,
	"load":    true,
	"faults":  true,
	"assert":  true,
	"fuzz":    true,
}

// Parse parses and validates raw torture.yaml bytes.
func Parse(raw []byte) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("torture.yaml: empty document")
	}
	doc := root.Content[0]
	present := map[string]bool{}
	for i := 0; i < len(doc.Content); i += 2 {
		key := doc.Content[i].Value
		if !topLevelKeys[key] {
			return nil, fmt.Errorf("torture.yaml: unknown top-level key %q (line %d)", key, doc.Content[i].Line)
		}
		present[key] = true
	}

	var rc rawConfig
	if err := yaml.Unmarshal(raw, &rc); err != nil {
		return nil, err
	}

	if rc.Target.Compose == "" {
		return nil, fmt.Errorf("torture.yaml: target.compose is required")
	}
	if rc.Target.Service == "" {
		return nil, fmt.Errorf("torture.yaml: target.service is required")
	}

	if present["egress"] {
		if rc.Egress.Default != "deny" {
			return nil, fmt.Errorf("torture.yaml: egress.default must be \"deny\", got %q", rc.Egress.Default)
		}
		for host, eh := range rc.Egress.Hosts {
			switch eh.Class {
			case "mock":
				if eh.From != "capture" && eh.From != "spec" {
					return nil, fmt.Errorf("torture.yaml: egress.hosts[%q]: class mock requires from: capture|spec", host)
				}
			case "real":
				if eh.MaxRPS <= 0 {
					return nil, fmt.Errorf("torture.yaml: egress.hosts[%q]: class real requires max_rps", host)
				}
			case "internal", "block":
				// no extra fields required
			default:
				return nil, fmt.Errorf("torture.yaml: egress.hosts[%q]: unknown class %q", host, eh.Class)
			}
		}
	}

	cfg := &Config{
		Version: rc.Version,
		Target:  rc.Target,
		Egress:  rc.Egress,
	}

	if present["load"] {
		if rc.Load.Model != "arrival_rate" {
			return nil, fmt.Errorf("torture.yaml: load.model must be \"arrival_rate\" (open model), got %q", rc.Load.Model)
		}

		seenPhase := map[string]bool{}
		for _, s := range rc.Load.Stages {
			if seenPhase[s.Phase] {
				return nil, fmt.Errorf("torture.yaml: load.stages: duplicate phase %q", s.Phase)
			}
			seenPhase[s.Phase] = true

			if (s.To == "") == (s.Hold == "") {
				return nil, fmt.Errorf("torture.yaml: load.stages[%q]: must specify exactly one of to: or hold:", s.Phase)
			}
		}

		var scenarios []Scenario
		for _, rs := range rc.Load.Scenarios {
			var flow []FlowStep
			for i, node := range rs.Flow {
				if node.Kind != yaml.MappingNode {
					return nil, fmt.Errorf("torture.yaml: load.scenarios[%q].flow[%d]: must be a mapping with method and path, not a bare string", rs.Name, i)
				}
				var fs FlowStep
				if err := node.Decode(&fs); err != nil {
					return nil, fmt.Errorf("torture.yaml: load.scenarios[%q].flow[%d]: %w", rs.Name, i, err)
				}
				if fs.Method == "" || fs.Path == "" {
					return nil, fmt.Errorf("torture.yaml: load.scenarios[%q].flow[%d]: must specify both method and path", rs.Name, i)
				}
				flow = append(flow, fs)
			}
			scenarios = append(scenarios, Scenario{Name: rs.Name, Weight: rs.Weight, Flow: flow})
		}

		cfg.Load = Load{
			Engine:    rc.Load.Engine,
			Model:     rc.Load.Model,
			Stages:    rc.Load.Stages,
			Scenarios: scenarios,
		}
	}

	if present["faults"] {
		phases := map[string]bool{}
		for _, s := range rc.Load.Stages {
			phases[s.Phase] = true
		}
		knownTargets := map[string]bool{rc.Target.Service: true}
		for host := range rc.Egress.Hosts {
			knownTargets[host] = true
		}

		for _, rf := range rc.Faults {
			if rf.At == "" || rf.Target == "" || len(rf.Inject) == 0 {
				return nil, fmt.Errorf("torture.yaml: faults[%q]: at:, target:, and inject: are all required", rf.Name)
			}

			if !atGrammar.MatchString(rf.At) {
				return nil, fmt.Errorf("torture.yaml: faults[%q]: at: %q does not match <phase>, <phase>+<duration>, or t=<duration>", rf.Name, rf.At)
			}
			if !strings.HasPrefix(rf.At, "t=") {
				phaseRef, _, _ := strings.Cut(rf.At, "+")
				if !phases[phaseRef] {
					return nil, fmt.Errorf("torture.yaml: faults[%q]: at: references undeclared phase %q", rf.Name, phaseRef)
				}
			}

			if !knownTargets[rf.Target] {
				return nil, fmt.Errorf("torture.yaml: faults[%q]: target %q is not a detected service or classified egress host", rf.Name, rf.Target)
			}

			var verb string
			for k := range rf.Inject {
				if _, isVerb := faultVerbModifiers[k]; isVerb {
					if verb != "" {
						return nil, fmt.Errorf("torture.yaml: faults[%q]: inject: must contain exactly one verb, found %q and %q", rf.Name, verb, k)
					}
					verb = k
				}
			}
			if verb == "" {
				return nil, fmt.Errorf("torture.yaml: faults[%q]: inject: contains no recognized verb", rf.Name)
			}

			allowedModifiers := faultVerbModifiers[verb]
			for k := range rf.Inject {
				if k == verb {
					continue
				}
				if !allowedModifiers[k] {
					return nil, fmt.Errorf("torture.yaml: faults[%q]: inject: modifier %q is not valid for verb %q", rf.Name, k, verb)
				}
			}

			if v, ok := rf.Inject["duplicate"]; ok {
				if err := checkFraction(rf.Name, "duplicate", v); err != nil {
					return nil, err
				}
			}
			if v, ok := rf.Inject["error_rate"]; ok {
				if err := checkFraction(rf.Name, "error_rate", v); err != nil {
					return nil, err
				}
			}
			if v, ok := rf.Inject["count"]; ok {
				if err := checkPositiveInt(rf.Name, "count", v); err != nil {
					return nil, err
				}
			}
			if v, ok := rf.Inject["workers"]; ok {
				if err := checkPositiveInt(rf.Name, "workers", v); err != nil {
					return nil, err
				}
			}

			cfg.Faults = append(cfg.Faults, Fault{
				Name:   rf.Name,
				At:     rf.At,
				For:    rf.For,
				Target: rf.Target,
				Verb:   verb,
				Inject: rf.Inject,
			})
		}
	}

	if len(rc.Assert) == 0 {
		return nil, fmt.Errorf("torture.yaml: assert: is required and must not be empty — a run that cannot fail is not a test")
	}
	cfg.Assert = rc.Assert

	cfg.Reset = rc.Reset
	if cfg.Reset.Command == "" {
		cfg.Reset.Command = defaultResetCommand
	}

	return cfg, nil
}
