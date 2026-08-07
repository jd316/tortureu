package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	tortureurun "github.com/jdb316/tortureu/internal/run"
	"github.com/jdb316/tortureu/internal/verdict"
)

// emitVerdict writes v to w — JSON when asJSON, otherwise verdict.Render's
// human text — and returns the process exit code (R-VER-7). Both branches
// read the same *verdict.Verdict; there is no second formatting path
// (R-VER-9). ExitCode is called through unmodified, so exit 4
// (inconclusive) is never coerced into 0 (R-VER-8).
func emitVerdict(v *verdict.Verdict, asJSON bool, w io.Writer) int {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
	} else {
		fmt.Fprint(w, verdict.Render(*v))
	}
	return verdict.ExitCode(*v)
}

// runRun is the `tortureu run` verb: parse torture.yaml, detect the stack,
// execute the scenario, emit the verdict (R-CLI-1).
func runRun(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "torture.yaml", "path to torture.yaml")
	noReset := fs.Bool("no-reset", false, "skip the reset step (R-CFG-20)")
	asJSON := fs.Bool("json", false, "print the verdict as JSON instead of the human rendering")
	toxiproxyURL := fs.String("toxiproxy-url", "http://localhost:8474", "Toxiproxy control-plane address")
	promURL := fs.String("prom-url", "", "Prometheus base URL; promql: asserts are skipped when empty")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	raw, err := os.ReadFile(*path)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu run: read %s: %v\n", *path, err)
		return 2
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu run: %v\n", err)
		return 2
	}

	sys, err := detect.Detect(cfg.Target.Compose)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu run: detect: %v\n", err)
		return 2
	}

	deps := tortureurun.NewRealDeps(*toxiproxyURL, *promURL)
	v := tortureurun.Run(cfg, *sys, deps, tortureurun.Options{NoReset: *noReset})
	return emitVerdict(v, *asJSON, stdout)
}
