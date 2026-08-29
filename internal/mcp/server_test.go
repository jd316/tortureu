package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/detect"
	"github.com/jd316/TortureU/internal/verdict"
)

// rpcCall builds one newline-terminated JSON-RPC request line.
func rpcCall(id int, method string, params any) []byte {
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	raw, err := json.Marshal(req)
	if err != nil {
		panic(err)
	}
	return append(raw, '\n')
}

// decodeResponses splits Serve's output buffer into one rpcResponse per
// line.
func decodeResponses(t *testing.T, out []byte) []rpcResponse {
	t.Helper()
	var resps []rpcResponse
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r rpcResponse
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("response line does not parse as JSON-RPC: %v\nline: %s", err, line)
		}
		resps = append(resps, r)
	}
	return resps
}

// serve runs Server.Serve over an in-memory request buffer and returns the
// decoded responses. This is the closest thing to a real end-to-end client
// test practical here: it exercises the actual newline-delimited JSON-RPC
// wire format both directions through the real Serve loop, just with an
// in-process io.Reader/io.Writer standing in for a real client's stdio
// pipe rather than a spawned subprocess and a vendored MCP client SDK —
// building or vendoring a full external client was judged out of scope for
// this task (see the task report).
func serve(t *testing.T, s *Server, requests ...[]byte) []rpcResponse {
	t.Helper()
	var in bytes.Buffer
	for _, r := range requests {
		in.Write(r)
	}
	var out bytes.Buffer
	if err := s.Serve(&in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	return decodeResponses(t, out.Bytes())
}

// spec: R-MCP-7
//
// Was written uncited (SPEC.md had no transport requirement at the time —
// see the task report's escalation); R-MCP-7 now names exactly this:
// newline-delimited JSON-RPC 2.0 on stdio, initialize supported. No
// behaviour change — this is traceability, not a fix.
func TestServer_Initialize_RespondsWithProtocolVersionAndCapabilities(t *testing.T) {
	s := NewServer()
	resps := serve(t, s, rpcCall(1, "initialize", map[string]any{"protocolVersion": "2024-11-05"}))

	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if resps[0].Error != nil {
		t.Fatalf("initialize returned an error: %+v", resps[0].Error)
	}
	result, ok := resps[0].Result.(map[string]any)
	if !ok {
		t.Fatalf("result is not an object: %#v", resps[0].Result)
	}
	if result["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v, want %v", result["protocolVersion"], protocolVersion)
	}
	if _, ok := result["capabilities"]; !ok {
		t.Error("initialize result has no capabilities field")
	}
	if _, ok := result["serverInfo"]; !ok {
		t.Error("initialize result has no serverInfo field")
	}
}

// spec: R-MCP-1
// spec: R-MCP-7
func TestServer_ToolsList_ReturnsExactlyFiveToolsWithSchemas(t *testing.T) {
	s := NewServer()
	resps := serve(t, s, rpcCall(1, "tools/list", nil))

	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("tools/list failed: %+v", resps)
	}
	// Round-trip through JSON to get at the "tools" array the same way a
	// real client would, rather than relying on Go-side struct identity.
	raw, _ := json.Marshal(resps[0].Result)
	var decoded struct {
		Tools []struct {
			Name        string      `json:"name"`
			Description string      `json:"description"`
			InputSchema InputSchema `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("tools/list result doesn't decode: %v", err)
	}
	if len(decoded.Tools) != 5 {
		t.Fatalf("tools/list returned %d tools, want exactly 5 (R-MCP-1)", len(decoded.Tools))
	}
	for _, tool := range decoded.Tools {
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema.Type != "object" {
			t.Errorf("tool %q inputSchema.type = %q, want \"object\"", tool.Name, tool.InputSchema.Type)
		}
	}
}

// spec: R-MCP-7
//
// Proves R-MCP-7's "unknown tool ... MUST return a JSON-RPC error rather
// than panicking" clause directly.
func TestServer_ToolsCall_UnknownToolReturnsJSONRPCErrorNotPanic(t *testing.T) {
	s := NewServer()
	resps := serve(t, s, rpcCall(1, "tools/call", map[string]any{"name": "delete_everything", "arguments": map[string]any{}}))

	if len(resps) != 1 {
		t.Fatalf("got %d responses, want 1", len(resps))
	}
	if resps[0].Error == nil {
		t.Fatal("calling an unknown tool returned no error — want a JSON-RPC error")
	}
	if resps[0].Result != nil {
		t.Error("an error response must not also carry a result")
	}
}

// spec: R-MCP-7
//
// Proves R-MCP-7's "parse error ... MUST return a JSON-RPC error rather
// than panicking or closing the stream" clause directly.
func TestServer_HandleLine_MalformedJSONReturnsParseErrorNotPanic(t *testing.T) {
	s := NewServer()
	resp := s.handleLine([]byte(`{not json`))
	if resp == nil || resp.Error == nil {
		t.Fatalf("malformed JSON must produce a JSON-RPC error response, got %+v", resp)
	}
	if resp.Error.Code != errParseError {
		t.Errorf("Error.Code = %d, want %d (parse error)", resp.Error.Code, errParseError)
	}
}

// spec: R-MCP-7
//
// Notification semantics are part of the "JSON-RPC 2.0" R-MCP-7 requires;
// this is the general JSON-RPC 2.0 spec's own rule, exercised here because
// a naive implementation could easily send a response for every line.
func TestServer_NotificationGetsNoResponse(t *testing.T) {
	s := NewServer()
	// A request with no "id" is a JSON-RPC notification: dispatched, but
	// never answered.
	line := []byte(`{"jsonrpc":"2.0","method":"tools/list"}`)
	if resp := s.handleLine(line); resp != nil {
		t.Errorf("notification (no id) produced a response, want none: %+v", resp)
	}
}

func writeComposeFile(t *testing.T, dir string) {
	t.Helper()
	compose := `
services:
  checkout-api:
    image: myorg/checkout-api:latest
    ports: ["8080:8080"]
`
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatalf("write docker-compose.yml: %v", err)
	}
}

func writeTortureYAML(t *testing.T, dir, composePath string) string {
	t.Helper()
	torture := `
version: 0
target:
  compose: ` + composePath + `
  service: checkout-api
  base_url: http://localhost:8080
load:
  model: arrival_rate
  stages:
    - phase: peak
      hold: 10rps
      for: 1s
assert:
  - http_req_duration: ["p(95)<500"]
`
	path := filepath.Join(dir, "torture.yaml")
	if err := os.WriteFile(path, []byte(torture), 0o644); err != nil {
		t.Fatalf("write torture.yaml: %v", err)
	}
	return path
}

// spec: R-MCP-7
//
// Exercises the real Serve loop end to end (initialize, then tools/call)
// against emit_k6_script — the one tool whose real dispatch path (read a
// file, parse it, compile) needs no Docker daemon, unlike run_experiment's.
// It is the "real end-to-end" case the coordinator asked for, to the
// extent practical without a live Docker daemon or a vendored MCP client
// SDK (see the serve() helper's comment and the task report).
func TestServer_ServeEndToEnd_EmitK6ScriptThroughTheWire(t *testing.T) {
	dir := t.TempDir()
	writeComposeFile(t, dir)
	composePath := filepath.Join(dir, "docker-compose.yml")
	tortureYAML := writeTortureYAML(t, dir, composePath)

	s := NewServer()
	resps := serve(t, s,
		rpcCall(1, "initialize", map[string]any{}),
		rpcCall(2, "tools/call", map[string]any{
			"name":      NameEmitK6Script,
			"arguments": map[string]any{"torture_yaml": tortureYAML},
		}),
	)

	if len(resps) != 2 {
		t.Fatalf("got %d responses, want 2", len(resps))
	}
	if resps[1].Error != nil {
		t.Fatalf("emit_k6_script call failed: %+v", resps[1].Error)
	}
	raw, _ := json.Marshal(resps[1].Result)
	var result toolCallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("tools/call result doesn't decode: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("content = %+v, want one text block", result.Content)
	}
	if !strings.Contains(result.Content[0].Text, "export") {
		t.Errorf("emit_k6_script content doesn't look like a k6 script: %s", result.Content[0].Text)
	}
}

// spec: R-MCP-2
// spec: R-MCP-3
// spec: R-MCP-7
//
// run_experiment's dispatch wrapper (callRunExperiment, dispatch.go) is a
// thin argument-parsing layer around the already-tested RunExperiment
// function; wiring a live run through the transport needs a real Docker
// daemon, Toxiproxy, and k6 binary, which internal/run's own tests already
// exercise separately and which this package's tests never required (see
// run_experiment_test.go's fakes). This test instead proves the transport
// preserves R-MCP-2/R-MCP-3 on its one Docker-independent path: an invalid
// torture_yaml surfaces as a JSON-RPC error before anything is executed,
// never a panic, and never a fabricated verdict.
func TestServer_ToolsCall_RunExperiment_InvalidTortureYAMLReturnsErrorNotPanic(t *testing.T) {
	dir := t.TempDir()
	badYAML := filepath.Join(dir, "torture.yaml")
	if err := os.WriteFile(badYAML, []byte("not: valid: torture: yaml: at: all: ["), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewServer()
	resps := serve(t, s, rpcCall(1, "tools/call", map[string]any{
		"name":      NameRunExperiment,
		"arguments": map[string]any{"torture_yaml": badYAML},
	}))

	if len(resps) != 1 || resps[0].Error == nil {
		t.Fatalf("invalid torture.yaml must return a JSON-RPC error, got %+v", resps)
	}
	if len(s.runs) != 0 {
		t.Error("a failed parse must not record a run — R-MCP-3 (verdict unmodified) presumes a verdict actually exists")
	}
}

// spec: R-VER-4
// spec: R-MCP-7
//
// Exercises explain_failure's transport path against a runRecord this test
// injects directly (package-internal field access), standing in for one
// run_experiment would have recorded — the Docker-independent way to prove
// tools/call("explain_failure") reaches ExplainFailure and returns its
// result, without requiring a live run first.
func TestServer_ToolsCall_ExplainFailure_ReachesToolViaStoredRunRecord(t *testing.T) {
	s := NewServer()
	s.runs["run-1"] = runRecord{
		verdict: &verdict.Verdict{
			RunID: "run-1",
			Findings: []verdict.Finding{{
				ID:    "f1",
				Broke: verdict.Broke{Assertion: "x", Observed: "y"},
				Cause: &verdict.Cause{Fault: "pg_slow", Target: "postgres"},
			}},
		},
		sys: detect.System{
			Deps: []detect.Dep{{Name: "postgres", Type: "postgresql", Clients: []string{"jackc/pgx"}}},
		},
	}

	resps := serve(t, s, rpcCall(1, "tools/call", map[string]any{
		"name":      NameExplainFailure,
		"arguments": map[string]any{"run_id": "run-1", "finding_id": "f1"},
	}))

	if len(resps) != 1 || resps[0].Error != nil {
		t.Fatalf("explain_failure call failed: %+v", resps)
	}
	raw, _ := json.Marshal(resps[0].Result)
	var result toolCallResult
	_ = json.Unmarshal(raw, &result)
	if len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "jackc/pgx") {
		t.Errorf("explain_failure result doesn't carry the expected candidate: %+v", result)
	}
}
