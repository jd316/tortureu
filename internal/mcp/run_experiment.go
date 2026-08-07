package mcp

import (
	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/run"
	"github.com/jdb316/tortureu/internal/verdict"
)

// RunExperiment is the sole MCP tool that executes anything (R-MCP-2): the
// only call to run.Run in this package is the one below (enforced by
// TestRunExperiment_IsTheOnlyCallerOfRunDotRun, which greps this package's
// production sources). It returns run.Run's verdict.Verdict unmodified
// (R-MCP-3) — no field is read, added, renamed, or stripped between run.Run
// returning and this function returning.
func RunExperiment(cfg *config.Config, sys detect.System, deps run.Deps, opts run.Options) *verdict.Verdict {
	return run.Run(cfg, sys, deps, opts)
}
