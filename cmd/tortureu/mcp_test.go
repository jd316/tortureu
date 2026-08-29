package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/mcp"
)

// spec: R-MCP-7
//
// The default `mcp` behaviour must be the real JSON-RPC stdio server, not
// the static surface listing this verb previously printed unconditionally.
// Feeding a real initialize request through stdin and checking the
// response is a valid JSON-RPC 2.0 document (and specifically not the
// static "MCP TOOL SURFACE" text) proves runMcp actually calls
// mcp.NewServer().Serve rather than fabricating output.
func TestMcpDefaultServesRealJSONRPCServer(t *testing.T) {
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	var stdout, stderr bytes.Buffer

	code := runMcp(stdin, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "MCP TOOL SURFACE") {
		t.Fatalf("default mcp printed the static surface listing instead of running the real server:\n%s", stdout.String())
	}

	line := strings.TrimSpace(stdout.String())
	var resp struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      int            `json:"id"`
		Result  map[string]any `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("stdout is not a JSON-RPC response: %v\nstdout: %q", err, stdout.String())
	}
	if resp.JSONRPC != "2.0" {
		t.Errorf("response jsonrpc = %q, want \"2.0\"", resp.JSONRPC)
	}
	if resp.Result == nil {
		t.Errorf("initialize did not return a result: %s", stdout.String())
	}
}

// spec: R-MCP-7
//
// The surface-listing escape hatch this task's earlier stub-mode work
// built must survive now that the real transport exists: `-list` prints
// the tool surface and exits 0, without touching stdin at all (so it never
// looks like a hang to someone debugging their assistant's config).
func TestMcpListFlagStillPrintsSurfaceInstead(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMcp(strings.NewReader(""), []string{"-list"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, want 0: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "MCP TOOL SURFACE") {
		t.Errorf("stdout = %q, want the tool surface listing", stdout.String())
	}
}

// spec: R-CLI-1
func TestMcpVerbViaMainDefaultsToListWhenAsked(t *testing.T) {
	var out, errb bytes.Buffer
	code := Main([]string{"mcp", "-list"}, &out, &errb)
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

// spec: R-MCP-7
//
// The server's blocking, no-progress-protocol behaviour for run_experiment
// must be documented somewhere a user configuring an assistant will see it
// before wiring `tortureu mcp` into that assistant's config — `mcp -h` is
// exactly that place.
func TestMcpHelpDocumentsBlockingRunExperimentBehaviour(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runMcp(strings.NewReader(""), []string{"-h"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (flag.ContinueOnError on -h)", code)
	}
	help := stderr.String()
	if !strings.Contains(help, "blocks") && !strings.Contains(help, "block") {
		t.Errorf("mcp -h does not document the blocking behaviour:\n%s", help)
	}
	if !strings.Contains(help, "stdin") {
		t.Errorf("mcp -h does not convey that the default reads from stdin:\n%s", help)
	}
}
