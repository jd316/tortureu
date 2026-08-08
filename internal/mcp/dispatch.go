package mcp

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/run"
)

// This file turns tools/call's raw JSON arguments into calls against the
// five tool functions (describe_system.go, propose_experiments.go,
// run_experiment.go, explain_failure.go, emit_k6_script.go) and marshals
// their results back to text. It does not change any of those five
// functions' behaviour — it is argument parsing and marshaling around
// them, the "framing" the coordinator asked for.

// describeSystemArgs is describe_system's tools/call arguments.
type describeSystemArgs struct {
	ComposePath string `json:"compose_path"`
}

func (s *Server) callDescribeSystem(raw json.RawMessage) (string, error) {
	var args describeSystemArgs
	if err := unmarshalArgs(raw, &args); err != nil {
		return "", err
	}
	if args.ComposePath == "" {
		args.ComposePath = "docker-compose.yml"
	}
	sys, err := detect.Detect(args.ComposePath)
	if err != nil {
		return "", fmt.Errorf("describe_system: detect %s: %w", args.ComposePath, err)
	}
	return marshalResult(DescribeSystem(sys))
}

// proposeExperimentsArgs is propose_experiments' tools/call arguments.
type proposeExperimentsArgs struct {
	ComposePath string `json:"compose_path"`
	Dir         string `json:"dir"`
}

func (s *Server) callProposeExperiments(raw json.RawMessage) (string, error) {
	var args proposeExperimentsArgs
	if err := unmarshalArgs(raw, &args); err != nil {
		return "", err
	}
	if args.ComposePath == "" {
		args.ComposePath = "docker-compose.yml"
	}
	if args.Dir == "" {
		args.Dir = "."
	}
	sys, err := detect.Detect(args.ComposePath)
	if err != nil {
		return "", fmt.Errorf("propose_experiments: detect %s: %w", args.ComposePath, err)
	}
	return marshalResult(ProposeExperiments(args.Dir, sys))
}

// runExperimentArgs is run_experiment's tools/call arguments. TortureYAML
// is required (InputSchema says so); the rest default the same way
// internal/run's own real dependency wiring does.
type runExperimentArgs struct {
	TortureYAML   string `json:"torture_yaml"`
	NoReset       bool   `json:"no_reset"`
	ToxiproxyURL  string `json:"toxiproxy_url"`
	PrometheusURL string `json:"prometheus_url"`
}

func (s *Server) callRunExperiment(raw json.RawMessage) (string, error) {
	var args runExperimentArgs
	if err := unmarshalArgs(raw, &args); err != nil {
		return "", err
	}
	if args.TortureYAML == "" {
		return "", fmt.Errorf("run_experiment: torture_yaml is required")
	}
	yamlBytes, err := os.ReadFile(args.TortureYAML)
	if err != nil {
		return "", fmt.Errorf("run_experiment: read %s: %w", args.TortureYAML, err)
	}
	cfg, err := config.Parse(yamlBytes)
	if err != nil {
		return "", fmt.Errorf("run_experiment: parse %s: %w", args.TortureYAML, err)
	}
	sys, err := detect.Detect(cfg.Target.Compose)
	if err != nil {
		return "", fmt.Errorf("run_experiment: detect %s: %w", cfg.Target.Compose, err)
	}

	deps := run.NewRealDeps(args.ToxiproxyURL, args.PrometheusURL)
	v := RunExperiment(cfg, *sys, deps, run.Options{NoReset: args.NoReset})

	s.mu.Lock()
	s.runs[v.RunID] = runRecord{verdict: v, sys: *sys}
	s.mu.Unlock()

	return marshalResult(v)
}

// explainFailureArgs is explain_failure's tools/call arguments.
type explainFailureArgs struct {
	RunID     string `json:"run_id"`
	FindingID string `json:"finding_id"`
}

func (s *Server) callExplainFailure(raw json.RawMessage) (string, error) {
	var args explainFailureArgs
	if err := unmarshalArgs(raw, &args); err != nil {
		return "", err
	}
	if args.RunID == "" || args.FindingID == "" {
		return "", fmt.Errorf("explain_failure: run_id and finding_id are both required")
	}

	s.mu.Lock()
	rec, ok := s.runs[args.RunID]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("explain_failure: no verdict with run_id %q from a prior run_experiment call in this session", args.RunID)
	}

	ex, err := ExplainFailure(rec.verdict, args.FindingID, &rec.sys)
	if err != nil {
		return "", fmt.Errorf("explain_failure: %w", err)
	}
	return marshalResult(ex)
}

// emitK6ScriptArgs is emit_k6_script's tools/call arguments.
type emitK6ScriptArgs struct {
	TortureYAML string `json:"torture_yaml"`
}

func (s *Server) callEmitK6Script(raw json.RawMessage) (string, error) {
	var args emitK6ScriptArgs
	if err := unmarshalArgs(raw, &args); err != nil {
		return "", err
	}
	if args.TortureYAML == "" {
		return "", fmt.Errorf("emit_k6_script: torture_yaml is required")
	}
	yamlBytes, err := os.ReadFile(args.TortureYAML)
	if err != nil {
		return "", fmt.Errorf("emit_k6_script: read %s: %w", args.TortureYAML, err)
	}
	cfg, err := config.Parse(yamlBytes)
	if err != nil {
		return "", fmt.Errorf("emit_k6_script: parse %s: %w", args.TortureYAML, err)
	}
	script, err := EmitK6Script(cfg)
	if err != nil {
		return "", fmt.Errorf("emit_k6_script: compile: %w", err)
	}
	return script, nil
}

// unmarshalArgs decodes raw tools/call arguments into dst. Absent
// arguments (raw == nil, a tool called with no "arguments" at all) decode
// as dst's zero value rather than an error — every argument here has a
// documented default or an explicit required-field check of its own.
func unmarshalArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// marshalResult renders a tool's structured Go result as the JSON text a
// toolCallResult content block carries.
func marshalResult(v any) (string, error) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(raw), nil
}
