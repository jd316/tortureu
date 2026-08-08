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
	// Every endpoint below defaults to "" and is left for NewRealDepsFull to
	// resolve: toxiproxyURL becomes NewRealDepsFull's own
	// "http://localhost:<ProxyControlPort>" default; mockURL and brokerURL
	// have no sensible default (a user's WireMock or broker is not
	// necessarily at any address we'd guess), so "" leaves the
	// corresponding Deps field nil and a run that then declares
	// error_rate/poison_pill/duplicate fails loudly (R-EXE-19) instead of
	// connecting to an invented address. None of these are hardcoded here.
	toxiproxyURL := fs.String("toxiproxy-url", "", "Toxiproxy control-plane address; defaults to the standard local overlay port")
	promURL := fs.String("prom-url", "", "Prometheus base URL; promql: asserts are skipped when empty")
	mockURL := fs.String("mock-url", "", "WireMock base URL, for error_rate faults against a class: mock host; empty fails loudly if declared (R-EXE-19)")
	brokerURL := fs.String("broker-url", "", "message broker base URL, for poison_pill/duplicate faults; empty fails loudly if declared (R-EXE-19)")
	// multiplier/allowRealTraffic default to the safe values (1x, no real
	// traffic): a user who passes nothing gets no widened blast radius.
	// egress.CheckMultiplier only relaxes when -allow-real-traffic is
	// explicitly set (R-DC2-4) — replay above 1x against a class: real
	// host requires deliberate consent, because blast radius scales with
	// the multiplier and the multiplier is exactly what makes replay
	// attractive.
	multiplier := fs.Float64("multiplier", 1, "replay rate multiplier applied against class: real hosts; only relevant with -allow-real-traffic")
	allowRealTraffic := fs.Bool("allow-real-traffic", false, "permit replay above 1x against a class: real host (R-DC2-4); without this, a multiplier above 1x against a real host aborts the run")
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

	deps := buildRealDeps(*toxiproxyURL, *promURL, *mockURL, *brokerURL)
	opts := buildRunOptions(*noReset, *allowRealTraffic, *multiplier)
	v := tortureurun.Run(cfg, *sys, deps, opts)
	return emitVerdict(v, *asJSON, stdout)
}

// buildRunOptions is the one call site that turns the run verb's flags into
// internal/run.Options. It exists as its own function, the same reasoning
// as buildRealDeps: a test must be able to prove -allow-real-traffic and
// -multiplier actually reach Options (R-DC2-4) instead of the wiring being
// asserted only by reading the source — this is the fourth time in this
// project a value existed and never reached its consumer.
func buildRunOptions(noReset, allowRealTraffic bool, multiplier float64) tortureurun.Options {
	return tortureurun.Options{
		NoReset:          noReset,
		AllowRealTraffic: allowRealTraffic,
		Multiplier:       multiplier,
	}
}

// buildRealDeps is the one call site that wires the four `run` endpoint
// flags into internal/run's real, live-infra Deps. It exists as its own
// function — rather than inlined at runRun's call site — so a test can
// prove the flags actually reach NewRealDepsFull (R-EXE-19) instead of the
// wiring being asserted only by reading the source: passing -mock-url or
// -broker-url must be observable as Deps.MockApplier / Deps.QueueApplier
// going from nil to non-nil, the same way internal/run's own tests observe
// NewRealDepsFull.
func buildRealDeps(toxiproxyURL, promURL, mockURL, brokerURL string) tortureurun.Deps {
	return tortureurun.NewRealDepsFull(toxiproxyURL, promURL, mockURL, brokerURL)
}
