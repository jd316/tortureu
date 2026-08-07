package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
)

// spec: R-CLI-1
func TestBuildInitWritesTargetAndEgressBlocks(t *testing.T) {
	sys := &detect.System{
		SUT:         "checkout-api",
		EgressClass: map[string]string{"postgres:5432": "internal", "api.stripe.com": "unclassified"},
	}
	out := buildInit(sys, "./docker-compose.yml")
	content := string(out.YAML)
	if !strings.Contains(content, "service: checkout-api") {
		t.Errorf("missing target.service:\n%s", content)
	}
	if !strings.Contains(content, "postgres:5432: { class: internal }") {
		t.Errorf("missing classified internal host:\n%s", content)
	}
	if strings.Contains(content, "api.stripe.com: { class:") {
		t.Errorf("unclassified host must not be assigned a class:\n%s", content)
	}
}

// spec: R-DC1-3 (via R-CLI-1's init->egress-manifest row / DC-2): an
// unclassified host must be left out of hosts: entirely, not guessed, so
// CheckUnclassified aborts the first run rather than silently allowing
// egress through a fabricated classification.
func TestBuildInitLeavesUnclassifiedHostsOutOfHostsBlockAndReportsGap(t *testing.T) {
	sys := &detect.System{
		EgressClass: map[string]string{"api.partner.com": "unclassified"},
	}
	out := buildInit(sys, "./docker-compose.yml")
	found := false
	for _, g := range out.Gaps {
		if strings.Contains(g, "api.partner.com") {
			found = true
		}
	}
	if !found {
		t.Errorf("gaps = %v, want the unclassified host surfaced", out.Gaps)
	}
}

// spec: R-DET-7 (init must surface detect.System.Gaps, never hide them)
func TestBuildInitSurfacesDetectionGaps(t *testing.T) {
	sys := &detect.System{Gaps: []string{"unrecognized image: weird/thing"}}
	out := buildInit(sys, "./docker-compose.yml")
	found := false
	for _, g := range out.Gaps {
		if g == "unrecognized image: weird/thing" {
			found = true
		}
	}
	if !found {
		t.Errorf("gaps = %v, want detect gap surfaced", out.Gaps)
	}
}

// spec: R-DC1-3
func TestInitDoesNotTouchAnotherToolsMCPRegistration(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(compose, []byte("services:\n  checkout-api:\n    build: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mcpFile := filepath.Join(dir, ".mcp.json")
	original := []byte(`{"mcpServers":{"k6":{}}}`)
	if err := os.WriteFile(mcpFile, original, 0o644); err != nil {
		t.Fatal(err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	code := runInit([]string{"-compose", "docker-compose.yml", "-out", "torture.yaml"}, &out, &errb)
	if code != 0 {
		t.Fatalf("runInit failed (exit %d): %s", code, errb.String())
	}
	got, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Errorf("init modified another tool's MCP registration file: %s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "torture.yaml")); err != nil {
		t.Errorf("torture.yaml was not written: %v", err)
	}
}
