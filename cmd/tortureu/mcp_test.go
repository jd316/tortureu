package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/mcp"
)

// spec: R-CLI-1
func TestMcpVerbExitsZeroAndListsSurface(t *testing.T) {
	var out, errb bytes.Buffer
	code := Main([]string{"mcp"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "MCP TOOL SURFACE") {
		t.Errorf("stdout = %q, want the tool surface listing", out.String())
	}
}

// spec: R-MCP-1
func TestMcpReportListsExactlyFiveTools(t *testing.T) {
	report := mcpSurfaceReport()
	for _, name := range []string{
		mcp.NameDescribeSystem,
		mcp.NameProposeExperiments,
		mcp.NameRunExperiment,
		mcp.NameExplainFailure,
		mcp.NameEmitK6Script,
	} {
		if !strings.Contains(report, name) {
			t.Errorf("report missing tool %q:\n%s", name, report)
		}
	}
	if len(mcp.Tools) != 5 {
		t.Errorf("internal/mcp.Tools has %d entries, want exactly 5 (R-MCP-1)", len(mcp.Tools))
	}
}

// spec: R-MCP-5 (R-DC1-1 noun rule): tool names/descriptions and this
// package's own surrounding text must not borrow k6's nouns (script, test,
// threshold). emit_k6_script is the sole, deliberate exception (R-DC1-2).
func TestMcpReportObeysTheNounRule(t *testing.T) {
	report := mcpSurfaceReport()
	// Each tool renders as one blank-line-separated block (name,
	// description, input shape); the emit_k6_script exception applies to
	// its whole block, not just the line naming it.
	for _, block := range strings.Split(report, "\n\n") {
		lower := strings.ToLower(block)
		if strings.Contains(lower, mcp.NameEmitK6Script) {
			continue // the deliberate escape-hatch exception (R-DC1-2)
		}
		for _, noun := range []string{"script", " test ", "threshold"} {
			if strings.Contains(lower, noun) {
				t.Errorf("block leaks a k6 noun %q outside the emit_k6_script exception: %q", noun, block)
			}
		}
	}
}

// spec: R-MCP-1
//
// runMcp must honestly report that no stdio transport exists — the
// coordinator was explicit that a half-built protocol is worse than a
// clean tool listing, and a caller must not be able to mistake this output
// for "you can connect now".
func TestMcpReportStatesNoTransportYet(t *testing.T) {
	report := mcpSurfaceReport()
	if !strings.Contains(report, "does not speak the MCP stdio JSON-RPC") {
		t.Errorf("report does not disclose the missing transport:\n%s", report)
	}
}
