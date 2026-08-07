package main

import (
	"bytes"
	"strings"
	"testing"
)

// spec: R-CLI-1
func TestStubVerbsExitTwoNotImplemented(t *testing.T) {
	for _, v := range []string{"smoke", "mcp", "check", "emit", "capture", "replay"} {
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
