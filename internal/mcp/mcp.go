// Package mcp implements TortureU's MCP surface: exactly five tools
// (R-MCP-1), no more, whose nouns are TortureU's own — experiment, fault,
// slo, verdict — never k6's (script, test, threshold), per DC-1 (R-DC1-1,
// R-MCP-5). emit_k6_script is the sole, deliberate exception (R-DC1-2): an
// escape hatch that returns a script and executes nothing.
//
// Transport: this package implements the tool layer only — the five Go
// functions (DescribeSystem, ProposeExperiments, RunExperiment,
// ExplainFailure, EmitK6Script) plus the Tools listing below. A full stdio
// JSON-RPC server was not built; see the task report for what remains.
//
// This package never claims the DC-2 default-deny egress guarantee
// (R-DC2-7) in a tool description or a returned document — that gate has
// not lifted, and the most damaging thing this surface could do is imply
// otherwise to a calling agent.
package mcp

// Tool is one entry TortureU's MCP surface exposes to a calling agent.
type Tool struct {
	Name        string
	Description string
}

// Tool name constants — TortureU's nouns, never k6's (R-DC1-1, R-MCP-5).
const (
	NameDescribeSystem     = "describe_system"
	NameProposeExperiments = "propose_experiments"
	NameRunExperiment      = "run_experiment"
	NameExplainFailure     = "explain_failure"
	NameEmitK6Script       = "emit_k6_script"
)

// Tools is the complete, fixed MCP surface (R-MCP-1): exactly five tools.
var Tools = []Tool{
	{
		Name:        NameDescribeSystem,
		Description: "Detected services, dependencies, external egress classification, and observability coverage. Read-only; no run required.",
	},
	{
		Name:        NameProposeExperiments,
		Description: "Ranked experiment fragments for the detected topology, returned in torture.yaml shape — not prose.",
	},
	{
		Name:        NameRunExperiment,
		Description: "Execute a named experiment and return its verdict document. The only tool in this surface that executes anything.",
	},
	{
		Name:        NameExplainFailure,
		Description: "Expand one verdict finding into its failure chain: fault, symptom, span chain, and candidate config surface (library + knobs). Stops short of a file:line fix — that last mile is the calling agent's.",
	},
	{
		Name:        NameEmitK6Script,
		Description: "Escape hatch: the compiled k6 script for a scenario torture.yaml cannot express. Returns script text only; performs no execution.",
	},
}
