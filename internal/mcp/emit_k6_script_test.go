package mcp

import (
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/k6"
)

// spec: R-DC1-2
func TestEmitK6Script_ReturnsCompiledScriptAndExecutesNothing(t *testing.T) {
	cfg := minimalRunConfig()

	script, err := EmitK6Script(cfg)
	if err != nil {
		t.Fatalf("EmitK6Script: %v", err)
	}
	if script == "" {
		t.Fatal("EmitK6Script returned an empty script")
	}
	want, err := k6.Compile(cfg)
	if err != nil {
		t.Fatalf("k6.Compile: %v", err)
	}
	if script != want {
		t.Errorf("EmitK6Script must return exactly internal/k6.Compile's output (the escape hatch, R-DC1-2), got a different script")
	}
	if !strings.Contains(script, "export") {
		t.Error("script doesn't look like a k6 script (no \"export\")")
	}
}
