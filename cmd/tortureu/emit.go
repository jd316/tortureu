package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/emit"
)

// runEmit is the `tortureu emit` verb (proposed R-CLI-8): generate a
// delegate-tier tool's config/command from torture.yaml. Output goes to
// stdout by default (TBD-2) so it composes with shell redirection, e.g.
// `tortureu emit pumba > chaos.sh` — the same convention `run -json` and
// `check contracts` already use for their primary output.
func runEmit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("emit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "torture.yaml", "path to torture.yaml")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintf(stderr, "usage: tortureu emit <tool>\nsupported tools: %s\n", strings.Join(emit.Tools(), ", "))
		return 2
	}
	tool := rest[0]

	raw, err := os.ReadFile(*path)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu emit: read %s: %v\n", *path, err)
		return 2
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu emit: %v\n", err)
		return 2
	}

	// Only the emitters that need a real dependency address (sysbench,
	// memtier, fio) pay for detection — the rest work from torture.yaml
	// alone and must not be made to fail with it. When detection is needed
	// and fails, say so rather than swallowing it: otherwise the emitter
	// looks like it found no dependency, when it was never told of one.
	var sys *detect.System
	if emit.NeedsSystem(tool) {
		// R-DET-19: target.service is authoritative and already validated, so
		// detection must not be left to re-derive it. On a stack with several
		// candidate build: services R-DET-19 correctly names none, and an empty
		// sys.SUT would silently degrade two things that key off it — the
		// audit-candidate join (R-VER-4) and the Jaeger service lookup
		// (R-VER-13) — both of which fail closed rather than loudly.
		s, derr := detect.DetectWithSUT(cfg.Target.Compose, cfg.Target.Service)
		sys = s
		if derr != nil {
			fmt.Fprintf(stderr, "tortureu emit: could not detect the system from %s: %v\n", cfg.Target.Compose, derr)
		}
	}

	out, err := emit.Emit(tool, cfg, sys)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu emit: %v\n", err)
		return 2
	}
	fmt.Fprint(stdout, out)
	return 0
}
