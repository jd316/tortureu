package mcp

import "encoding/json"

// JSON-RPC 2.0 envelope (https://www.jsonrpc.org/specification). MCP's
// stdio transport frames each message as one JSON-RPC object per line
// (newline-delimited, no Content-Length header) — see server.go's Serve.

// rpcRequest is one incoming line. ID is raw so it round-trips whatever
// type the client sent (string or number) unchanged into the response; its
// absence (nil) marks a JSON-RPC notification, which gets no response.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse is one outgoing line: exactly one of Result/Error is set,
// never both (the JSON-RPC spec forbids both; omitempty keeps whichever is
// nil out of the wire form).
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is a JSON-RPC error object. Codes below -32000 are the
// JSON-RPC-reserved ones; -32000 is this server's one application-error
// code for a tool that failed for a domain reason (bad path, parse error,
// unknown finding id, ...) rather than a malformed request.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	errParseError     = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errApplicationErr = -32000
)
