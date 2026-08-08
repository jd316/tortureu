package mcp

// PropertySchema is one field of a tool's JSON Schema input. It is kept
// deliberately small — type, description, and an optional default — since
// every current tool's parameters are flat strings/bools with no nesting.
type PropertySchema struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Default     any    `json:"default,omitempty"`
}

// InputSchema is a minimal JSON Schema object, the shape tools/list must
// return per property (name) for each tool so a calling agent knows what
// it must supply before calling — in particular, which paths MUST already
// exist on disk, rather than that surfacing only as a confusing call-time
// error.
type InputSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]PropertySchema `json:"properties,omitempty"`
	Required   []string                  `json:"required,omitempty"`
}
