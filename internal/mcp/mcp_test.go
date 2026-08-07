package mcp

import (
	"strings"
	"testing"
)

// spec: R-MCP-1
func TestTools_ExactlyFive(t *testing.T) {
	if len(Tools) != 5 {
		t.Fatalf("len(Tools) = %d, want exactly 5 (R-MCP-1)", len(Tools))
	}
	want := map[string]bool{
		"describe_system":     true,
		"propose_experiments": true,
		"run_experiment":      true,
		"explain_failure":     true,
		"emit_k6_script":      true,
	}
	got := map[string]bool{}
	for _, tool := range Tools {
		got[tool.Name] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("Tools is missing %q (R-MCP-1)", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("Tools has unexpected extra tool %q (R-MCP-1: exactly five)", name)
		}
	}
}

// spec: R-MCP-5
func TestTools_NamesSatisfyDC1NounRule(t *testing.T) {
	k6Nouns := []string{"script", "test", "threshold"}
	for _, tool := range Tools {
		for _, noun := range k6Nouns {
			if strings.Contains(tool.Name, noun) && tool.Name != NameEmitK6Script {
				t.Errorf("tool %q contains k6 noun %q — DC-1/R-DC1-1 forbids this outside the emit_k6_script escape hatch (R-DC1-2)", tool.Name, noun)
			}
		}
	}
}

// spec: R-DC2-7
func TestTools_DescriptionsNeverClaimDC2Guarantee(t *testing.T) {
	forbidden := []string{"guarantee", "cannot reach the internet", "guarantees"}
	for _, tool := range Tools {
		lower := strings.ToLower(tool.Description)
		for _, phrase := range forbidden {
			if strings.Contains(lower, phrase) {
				t.Errorf("tool %q description claims the DC-2 guarantee (%q) — R-DC2-7 forbids this until the topology overlay is proven end to end", tool.Name, phrase)
			}
		}
	}
}
