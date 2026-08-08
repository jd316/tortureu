package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/emit"
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
		fmt.Fprintf(stderr, "usage: tortureu emit <tool>\nsupported tools: %s\n", strings.Join(emit.Tools, ", "))
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

	out, err := emit.Emit(tool, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu emit: %v\n", err)
		return 2
	}
	fmt.Fprint(stdout, out)
	return 0
}
