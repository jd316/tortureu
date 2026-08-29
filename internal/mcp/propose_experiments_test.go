package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/jd316/tortureu/internal/detect"
)

// fragmentDoc is enough of torture.yaml's faults: shape (SPEC.md §4.4) to
// prove a Fragment decodes as structured fault data, not prose (R-MCP-4).
type fragmentDoc struct {
	Faults []struct {
		Name   string         `yaml:"name"`
		At     string         `yaml:"at"`
		Target string         `yaml:"target"`
		Inject map[string]any `yaml:"inject"`
	} `yaml:"faults"`
}

// spec: R-MCP-4
func TestProposeExperiments_ReturnsTortureYAMLFragmentsNotProse(t *testing.T) {
	dir := t.TempDir()
	// A real pgx construction site with no timeout, so doctor.Audit confirms
	// a genuine (Determined=true, Present=false) gap to propose an
	// experiment for.
	writeFile(t, dir, "main.go", `package main

import "github.com/jackc/pgx/v5/pgxpool"

func connect() {
	pgxpool.New(ctx, "postgres://x")
}
`)
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "postgres", Type: "postgresql", Address: "postgres:5432", Clients: []string{"github.com/jackc/pgx/v5"}},
		},
	}

	proposals := ProposeExperiments(dir, sys)
	if len(proposals) == 0 {
		t.Fatal("ProposeExperiments returned no proposals for a detected client library")
	}
	for _, p := range proposals {
		var doc fragmentDoc
		if err := yaml.Unmarshal([]byte(p.Fragment), &doc); err != nil {
			t.Fatalf("proposal %q Fragment does not parse as torture.yaml faults: shape (R-MCP-4 forbids prose): %v\nfragment:\n%s", p.Name, err, p.Fragment)
		}
		if len(doc.Faults) != 1 {
			t.Fatalf("proposal %q Fragment decoded to %d faults, want exactly 1 structured fault fragment", p.Name, len(doc.Faults))
		}
		f := doc.Faults[0]
		if f.Name == "" || f.At == "" || f.Target == "" || len(f.Inject) == 0 {
			t.Errorf("proposal %q Fragment fault missing required fields: %+v", p.Name, f)
		}
	}
}

// spec: R-MCP-4
func TestProposeExperiments_RanksConfirmedGapsAboveUndetermined(t *testing.T) {
	dir := t.TempDir()
	// No Go source at all: doctor.Audit cannot find kafka's construction
	// site in its table (kafka isn't in doctor's known-site table), so its
	// finding is Determined=false — while a confirmed-missing-timeout
	// postgres finding (real pgx call, no timeout) should still rank first.
	writeFile(t, dir, "main.go", `package main

import "github.com/jackc/pgx/v5/pgxpool"

func connect() {
	pgxpool.New(ctx, "postgres://x")
}
`)
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "postgres", Type: "postgresql", Address: "postgres:5432", Clients: []string{"github.com/jackc/pgx/v5"}},
			{Name: "broker", Type: "kafka", Address: "broker:9092", Clients: []string{"segmentio/kafka-go"}},
		},
	}

	proposals := ProposeExperiments(dir, sys)
	if len(proposals) < 2 {
		t.Fatalf("expected at least 2 proposals, got %d", len(proposals))
	}
	for i := 1; i < len(proposals); i++ {
		if proposals[i-1].Rank > proposals[i].Rank {
			t.Fatalf("proposals are not sorted by ascending rank: %+v", proposals)
		}
	}
	if proposals[0].Rank != 1 {
		t.Errorf("highest-ranked proposal Rank = %d, want 1 (a confirmed static gap ranks first)", proposals[0].Rank)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
}
