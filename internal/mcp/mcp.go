// Package mcp implements TortureU's MCP surface: exactly five tools
// (R-MCP-1), no more, whose nouns are TortureU's own — experiment, fault,
// slo, verdict — never k6's (script, test, threshold), per DC-1 (R-DC1-1,
// R-MCP-5). emit_k6_script is the sole, deliberate exception (R-DC1-2): an
// escape hatch that returns a script and executes nothing.
//
// Transport: server.go implements a stdio JSON-RPC 2.0 server (initialize,
// tools/list, tools/call) over the same five tool functions
// (DescribeSystem, ProposeExperiments, RunExperiment, ExplainFailure,
// EmitK6Script). See the task report for what is and isn't covered.
//
// R-DC2-7 has lifted (per the coordinator, the topology overlay is now
// proven end to end), so run_experiment's description below states the
// enforcement factually — refusal on unclassified egress, topological
// isolation — without hype language ("guarantee", "cannot reach the
// internet"); TestTools_DescriptionsNeverClaimDC2Guarantee still guards
// against that wording creeping back in.
package mcp

// Tool is one entry TortureU's MCP surface exposes to a calling agent.
type Tool struct {
	Name        string
	Description string
	InputSchema InputSchema
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
// Every InputSchema states plainly which paths must already exist on disk
// (torture.yaml, docker-compose.yml) rather than leaving that to surface as
// a confusing error at call time.
var Tools = []Tool{
	{
		Name:        NameDescribeSystem,
		Description: "Detected services, dependencies, external egress classification, observability coverage, and tier-labelled registry suggestions for the delegate/know tiers (R-MCP-6). Read-only; no run required.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]PropertySchema{
				"compose_path": {
					Type:        "string",
					Description: "Path to an existing docker-compose.yml to detect against.",
					Default:     "docker-compose.yml",
				},
			},
		},
	},
	{
		Name:        NameProposeExperiments,
		Description: "Ranked experiment fragments for the detected topology, returned in torture.yaml shape — not prose.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]PropertySchema{
				"compose_path": {
					Type:        "string",
					Description: "Path to an existing docker-compose.yml to detect against.",
					Default:     "docker-compose.yml",
				},
				"dir": {
					Type:        "string",
					Description: "Existing repo directory to scope doctor's bounded source inspection to (R-AUD-5).",
					Default:     ".",
				},
			},
		},
	},
	{
		Name: NameRunExperiment,
		Description: "Execute a named experiment and return its verdict document. The only tool in this surface that executes anything. " +
			"Requires a live Docker daemon and can take minutes; this call blocks until the run finishes or fails — there is no progress " +
			"notification, so a long wait is expected, not a hang. Refuses to start if any reachable external host is unclassified " +
			"(exit code 3); the target service runs isolated in a network whose only egress path is the TortureU proxy, enforced by " +
			"container network topology, not a policy check.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]PropertySchema{
				"torture_yaml": {
					Type:        "string",
					Description: "Path to an existing, already-valid torture.yaml (see internal/config.Parse). Parse errors are returned as a JSON-RPC error, not a partial run.",
				},
				"no_reset": {
					Type:        "boolean",
					Description: "Skip the reset: command before load begins.",
					Default:     false,
				},
				"toxiproxy_url": {
					Type:        "string",
					Description: "Toxiproxy control-plane URL. Defaults to the TortureU proxy overlay's fixed control port.",
				},
				"prometheus_url": {
					Type:        "string",
					Description: "Prometheus URL for promql: asserts. Omit to skip promql: evaluation entirely (not a silent pass — see torture.yaml's assert: rules).",
				},
			},
			Required: []string{"torture_yaml"},
		},
	},
	{
		Name: NameExplainFailure,
		Description: "Expand one verdict finding into its failure chain: fault, symptom, span chain, and candidate config surface (library + knobs). " +
			"Stops short of a file:line fix — that last mile is the calling agent's. run_id must come from a verdict this same server process " +
			"already returned from run_experiment; verdicts are not persisted across restarts.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]PropertySchema{
				"run_id": {
					Type:        "string",
					Description: "run_id from a verdict.Verdict this server already returned via run_experiment in this session.",
				},
				"finding_id": {
					Type:        "string",
					Description: "One finding's id from that verdict's findings list.",
				},
			},
			Required: []string{"run_id", "finding_id"},
		},
	},
	{
		Name:        NameEmitK6Script,
		Description: "Escape hatch: the compiled k6 script for a scenario torture.yaml cannot express. Returns script text only; performs no execution.",
		InputSchema: InputSchema{
			Type: "object",
			Properties: map[string]PropertySchema{
				"torture_yaml": {
					Type:        "string",
					Description: "Path to an existing, already-valid torture.yaml to compile.",
				},
			},
			Required: []string{"torture_yaml"},
		},
	},
}
