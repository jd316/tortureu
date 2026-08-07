package main

import (
	"bytes"
	"strings"
	"testing"
)

// spec: R-CLI-1
func TestStubVerbsExitTwoNotImplemented(t *testing.T) {
	// mcp graduated from stub to real (Task 8 addendum): internal/mcp ships
	// the tool layer, and this package now wires the `mcp` verb to it —
	// see mcp_test.go for its own coverage.
	for _, v := range []string{"smoke", "check", "emit", "capture", "replay"} {
		var out, errb bytes.Buffer
		code := Main([]string{v}, &out, &errb)
		if code != 2 {
			t.Errorf("%s: exit = %d, want 2", v, code)
		}
		if !strings.Contains(errb.String(), "not implemented in v0") {
			t.Errorf("%s: stderr = %q, want \"not implemented in v0\"", v, errb.String())
		}
	}
}
