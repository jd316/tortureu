package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/jd316/TortureU/internal/mcp"
)

// mcpInputShape is a short, honest note on what each internal/mcp tool
// takes, shown by `-list` for a human skimming the surface without a
// JSON-RPC client. It documents the real Go signatures in internal/mcp
// (DescribeSystem, ProposeExperiments, RunExperiment, ExplainFailure,
// EmitK6Script) — nothing here is invented. `tools/list` over the wire
// returns the authoritative JSON-Schema version of this; this is a
// convenience mirror for a terminal, not a second protocol surface.
var mcpInputShape = map[string]string{
	mcp.NameDescribeSystem:     "compose path — detects the system; no execution",
	mcp.NameProposeExperiments: "compose path — ranked experiment fragments from detected topology + resilience audit",
	mcp.NameRunExperiment:      "torture.yaml path — executes the scenario; the only tool in this surface that runs anything",
	mcp.NameExplainFailure:     "a verdict document plus a finding id — expands one finding into its failure chain",
	mcp.NameEmitK6Script:       "torture.yaml path — escape hatch; returns k6 script text, executes nothing",
}

// mcpHelpPreamble is shown by `tortureu mcp -h`. R-MCP-7 requires the
// server's one-request-at-a-time, no-progress-protocol blocking behaviour
// to be documented "so the behaviour reads as expected rather than hung" —
// this is the place a user wiring `tortureu mcp` into an assistant's MCP
// config is most likely to look before doing so.
const mcpHelpPreamble = `tortureu mcp: serve TortureU's 5-tool MCP surface (R-MCP-1) as newline-
delimited JSON-RPC 2.0 on stdio (R-MCP-7): initialize, tools/list, tools/call.

By default this reads requests from stdin and writes responses to stdout —
that is what an assistant's MCP client expects, and it will look like a
hang in an interactive terminal, because it is waiting on stdin. Use -list
to print the tool surface and exit instead, for a quick look without
speaking JSON-RPC.

run_experiment executes a real run against a live Docker daemon and can
take minutes. The server handles one request at a time and implements no
progress notification, so it blocks until that call returns — this is
expected, not wedged, and a second concurrent call needs a second process.
`

// runMcp is the `tortureu mcp` verb: by default, the real JSON-RPC stdio
// server (R-MCP-7); `-list` prints the tool surface and exits instead, so
// a person debugging their assistant's config gets a discoverable answer
// rather than an apparent hang on stdin.
func runMcp(stdin io.Reader, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	list := fs.Bool("list", false, "print the tool surface and exit, instead of serving JSON-RPC on stdio")
	fs.Usage = func() {
		fmt.Fprint(stderr, mcpHelpPreamble)
		fmt.Fprintln(stderr)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *list {
		fmt.Fprint(stdout, mcpSurfaceReport())
		return 0
	}

	if err := mcp.NewServer().Serve(stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "tortureu mcp: %v\n", err)
		return 2
	}
	return 0
}

// mcpSurfaceReport renders internal/mcp.Tools (R-MCP-1: exactly five,
// R-MCP-5/R-DC1-1: TortureU's own nouns, emit_k6_script the sole named
// exception) plus each tool's input shape — the `-list` escape hatch, not
// the default behaviour now that R-MCP-7's real transport exists.
func mcpSurfaceReport() string {
	s := "MCP TOOL SURFACE (-list: this printout; default: JSON-RPC on stdio, R-MCP-7)\n\n"
	for _, t := range mcp.Tools {
		s += fmt.Sprintf("  %s\n    %s\n    input: %s\n\n", t.Name, t.Description, mcpInputShape[t.Name])
	}
	return s
}
