package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/jdb316/tortureu/internal/mcp"
)

// mcpInputShape is a short, honest note on what each internal/mcp tool
// takes, for an operator or agent deciding whether to reach for tortureu's
// MCP surface before a stdio transport exists to carry it. It documents
// the real Go signatures in internal/mcp (DescribeSystem, ProposeExperiments,
// RunExperiment, ExplainFailure, EmitK6Script) — nothing here is invented,
// and nothing here executes anything.
var mcpInputShape = map[string]string{
	mcp.NameDescribeSystem:     "compose path — detects the system; no execution",
	mcp.NameProposeExperiments: "compose path — ranked experiment fragments from detected topology + resilience audit",
	mcp.NameRunExperiment:      "torture.yaml path — executes the scenario; the only tool in this surface that runs anything",
	mcp.NameExplainFailure:     "a verdict document plus a finding id — expands one finding into its failure chain",
	mcp.NameEmitK6Script:       "torture.yaml path — escape hatch; returns k6 script text, executes nothing",
}

// runMcp is the `tortureu mcp` verb.
//
// internal/mcp deliberately ships the tool layer only — five Go functions
// plus the Tools listing (R-MCP-1) — and not a stdio JSON-RPC transport
// (initialize handshake, capability negotiation, tools/list, tools/call
// framing). Building that transport well enough for a real agent to
// connect to is a protocol implementation in its own right, not glue code;
// per instructions, a half-built version of it is worse than this: it
// lists the tool surface honestly and says plainly what is still missing,
// rather than accepting a connection an agent can then fail against in
// confusing ways.
func runMcp(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	fmt.Fprint(stdout, mcpSurfaceReport())
	return 0
}

// mcpSurfaceReport renders internal/mcp.Tools (R-MCP-1: exactly five,
// R-MCP-5/R-DC1-1: TortureU's own nouns, emit_k6_script the sole named
// exception) plus each tool's input shape, and states clearly that no
// stdio transport exists yet.
func mcpSurfaceReport() string {
	s := "MCP TOOL SURFACE (tool layer only — no stdio transport yet, see below)\n\n"
	for _, t := range mcp.Tools {
		s += fmt.Sprintf("  %s\n    %s\n    input: %s\n\n", t.Name, t.Description, mcpInputShape[t.Name])
	}
	s += "This lists the tool surface; it does not speak the MCP stdio JSON-RPC\n" +
		"protocol (initialize handshake, tools/list, tools/call framing). An agent\n" +
		"cannot connect to `tortureu mcp` over stdio yet. internal/mcp implements\n" +
		"the dispatchable Go functions above; wiring a compliant transport around\n" +
		"them is unbuilt.\n"
	return s
}
