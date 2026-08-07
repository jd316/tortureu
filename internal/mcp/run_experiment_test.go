package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/egress"
	"github.com/jdb316/tortureu/internal/fault"
	"github.com/jdb316/tortureu/internal/run"
	"github.com/jdb316/tortureu/internal/verdict"
)

type fakeResetter struct{ called bool }

func (f *fakeResetter) Reset(command string) error { f.called = true; return nil }

type fakeTopology struct{ called bool }

func (f *fakeTopology) Apply(composePath string, top egress.Topology, externalHosts, internalHosts []string) error {
	f.called = true
	return nil
}

type fakeLoadHandle struct {
	markers chan run.PhaseMarker
	done    chan run.LoadResult
	errCh   chan error
}

func (h *fakeLoadHandle) Markers() <-chan run.PhaseMarker { return h.markers }
func (h *fakeLoadHandle) Done() <-chan run.LoadResult     { return h.done }
func (h *fakeLoadHandle) Err() <-chan error               { return h.errCh }

type fakeLoadRunner struct{ handle run.LoadHandle }

func (f *fakeLoadRunner) Start(script string) (run.LoadHandle, error) { return f.handle, nil }

type fakeApplier struct{}

func (fakeApplier) ApplyToxic(name string, t fault.Toxic) (func() error, error) {
	return func() error { return nil }, nil
}
func (fakeApplier) ApplyDocker(name string, d fault.DockerAction) (func() error, error) {
	return func() error { return nil }, nil
}

func minimalRunConfig() *config.Config {
	return &config.Config{
		Target: config.Target{Compose: "docker-compose.yml", Service: "checkout-api", BaseURL: "http://localhost:8080"},
		Egress: config.Egress{Default: "deny", Hosts: map[string]config.EgressHost{}},
		Load: config.Load{
			Model:  "arrival_rate",
			Stages: []config.Stage{{Phase: "peak", Hold: "10rps", For: "1s"}},
		},
		Assert: []config.AssertEntry{{"http_req_duration": []string{"p(95)<500"}}},
		Reset:  config.Reset{Command: "true"},
	}
}

// spec: R-MCP-3
func TestRunExperiment_ReturnsVerdictUnmodified(t *testing.T) {
	cfg := minimalRunConfig()
	sys := detect.System{}

	handle := &fakeLoadHandle{
		markers: make(chan run.PhaseMarker, 1),
		done:    make(chan run.LoadResult, 1),
		errCh:   make(chan error, 1),
	}
	handle.done <- run.LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}

	deps := run.Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: handle},
		Applier:  fakeApplier{},
	}

	v := RunExperiment(cfg, sys, deps, run.Options{NoReset: true})
	if v == nil {
		t.Fatal("RunExperiment returned nil")
	}
	if v.Status != verdict.StatusPass {
		t.Errorf("Status = %q, want pass — RunExperiment must return run.Run's verdict unmodified (R-MCP-3)", v.Status)
	}
	if v.Reset != "skipped" {
		t.Errorf("Reset = %q, want \"skipped\" (NoReset was set) — a field changed between run.Run and RunExperiment would violate R-MCP-3", v.Reset)
	}
	if v.Scenario != "" {
		// Run doesn't set Scenario from cfg (no such field maps directly);
		// this just documents that RunExperiment adds nothing of its own.
	}
}

// spec: R-MCP-2
func TestRunExperiment_IsTheOnlyCallerOfRunDotRun(t *testing.T) {
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	count := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, "_test.go") || !strings.HasSuffix(name, ".go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		count += strings.Count(string(raw), "run.Run(")
	}
	if count != 1 {
		t.Errorf("run.Run( appears %d times across internal/mcp production files, want exactly 1 — R-MCP-2 requires run_experiment to be the only tool that executes anything", count)
	}
}
