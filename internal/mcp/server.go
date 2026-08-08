package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/verdict"
)

// protocolVersion is the MCP protocol date-version this server speaks.
const protocolVersion = "2024-11-05"

// serverName/serverVersion identify this process in the initialize
// handshake's serverInfo.
const (
	serverName    = "tortureu"
	serverVersion = "0.1.0"
)

// runRecord is what explain_failure needs back for a run_id run_experiment
// already returned: the verdict itself, plus the detect.System snapshot
// used for that run, since ExplainFailure's candidate lookup needs both.
// Kept in memory only — this server does not persist verdicts across
// restarts (stated in NameExplainFailure's InputSchema description), since
// no persistence layer exists anywhere in the built packages and inventing
// one is outside this task's scope.
type runRecord struct {
	verdict *verdict.Verdict
	sys     detect.System
}

// Server is the stdio JSON-RPC transport around this package's five tool
// functions. The zero value is ready to use.
type Server struct {
	mu   sync.Mutex
	runs map[string]runRecord
}

// NewServer constructs a ready-to-Serve Server.
func NewServer() *Server {
	return &Server{runs: map[string]runRecord{}}
}

// Serve reads newline-delimited JSON-RPC requests from r and writes
// newline-delimited JSON-RPC responses to w until r is exhausted or
// returns an error. Requests are handled one at a time, in the order
// received — there is no concurrent dispatch and no per-call timeout, so a
// long-running run_experiment call blocks the loop until it finishes: the
// server simply doesn't read or answer anything else while it's in
// flight. That is a deliberate, documented limitation, not an accident:
// MCP's JSON-RPC stdio transport has no standard progress-notification
// this server implements, so the honest, predictable behaviour is "nothing
// else happens until this returns" rather than a partial pipelining
// scheme that could make a stalled call look identical to a slow one.
// Callers that need concurrent calls must run multiple server processes.
//
// A JSON-RPC notification (a request with no "id") is dispatched the same
// as any other method but produces no response line, per the JSON-RPC 2.0
// spec.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	// A verdict document (with metrics/findings) can comfortably exceed
	// bufio.Scanner's 64KiB default; allow up to 16MiB per line.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		trimmed := trimSpaceBytes(line)
		if len(trimmed) == 0 {
			continue
		}
		resp := s.handleLine(append([]byte(nil), line...))
		if resp == nil {
			continue // notification: no response line
		}
		if err := writeLine(w, resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// handleLine parses and dispatches one JSON-RPC message. It never panics:
// any failure — malformed JSON, unknown method, bad params, or a tool
// function returning an error — becomes an rpcError on the response,
// never a crash of the server process (the coordinator's explicit
// requirement). Returns nil for a notification (no "id").
func (s *Server) handleLine(line []byte) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return &rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: errParseError, Message: "parse error: " + err.Error()}}
	}

	result, rpcErr := s.dispatch(req.Method, req.Params)

	if len(req.ID) == 0 {
		return nil // notification
	}
	resp := &rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		resp.Result = result
	}
	return resp
}

func (s *Server) dispatch(method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return s.handleInitialize()
	case "notifications/initialized":
		return nil, nil // MCP client ack; nothing to do, and it's a notification anyway
	case "tools/list":
		return s.handleToolsList()
	case "tools/call":
		return s.handleToolsCall(params)
	default:
		return nil, &rpcError{Code: errMethodNotFound, Message: "method not found: " + method}
	}
}

// handleInitialize answers the MCP initialize handshake: protocol version,
// this server's capabilities (tools only — no resources, no prompts, no
// sampling), and identification.
func (s *Server) handleInitialize() (any, *rpcError) {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": serverVersion,
		},
	}, nil
}

// mcpToolListing is one entry in tools/list's result, the MCP wire shape
// for a tool (name/description/inputSchema) — Tool itself stays this
// package's own Go-side type so DC-1's tests keep checking exactly what
// ships.
type mcpToolListing struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// handleToolsList returns exactly the five tools in Tools (R-MCP-1), each
// with its input schema.
func (s *Server) handleToolsList() (any, *rpcError) {
	listing := make([]mcpToolListing, 0, len(Tools))
	for _, t := range Tools {
		listing = append(listing, mcpToolListing{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return map[string]any{"tools": listing}, nil
}

// toolCallParams is tools/call's params shape (MCP spec): which tool, and
// its arguments.
type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// toolCallResult is tools/call's result shape (MCP spec): content blocks,
// here always a single JSON-text block carrying the tool's structured
// output, since none of these five tools produce anything else worth
// rendering as a separate content type.
type toolCallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// handleToolsCall dispatches to one of the five tool functions and wraps
// its result as a text content block. A failure anywhere in this path
// (unknown tool name, bad arguments, a tool function returning an error)
// becomes an rpcError, never a panic.
func (s *Server) handleToolsCall(params json.RawMessage) (any, *rpcError) {
	var call toolCallParams
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &rpcError{Code: errInvalidParams, Message: "invalid tools/call params: " + err.Error()}
	}

	text, err := s.callTool(call.Name, call.Arguments)
	if err != nil {
		return nil, &rpcError{Code: errApplicationErr, Message: err.Error()}
	}
	return toolCallResult{Content: []contentBlock{{Type: "text", Text: text}}}, nil
}

// callTool runs the named tool against raw JSON arguments and returns its
// result pre-marshaled to text — the actual argument parsing and tool
// invocation lives in dispatch.go, kept separate from this file's pure
// JSON-RPC framing.
func (s *Server) callTool(name string, args json.RawMessage) (string, error) {
	switch name {
	case NameDescribeSystem:
		return s.callDescribeSystem(args)
	case NameProposeExperiments:
		return s.callProposeExperiments(args)
	case NameRunExperiment:
		return s.callRunExperiment(args)
	case NameExplainFailure:
		return s.callExplainFailure(args)
	case NameEmitK6Script:
		return s.callEmitK6Script(args)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

func writeLine(w io.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = w.Write(raw)
	return err
}

func trimSpaceBytes(b []byte) []byte {
	start := 0
	for start < len(b) && isSpaceByte(b[start]) {
		start++
	}
	end := len(b)
	for end > start && isSpaceByte(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}
