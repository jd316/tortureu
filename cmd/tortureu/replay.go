package main

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/jdb316/tortureu/internal/capture"
	"github.com/jdb316/tortureu/internal/egress"
)

// runReplay is the `tortureu replay` verb (R-CLI-10): read a cassette
// written by `tortureu capture` and drive it as load against -target.
//
// R-DC2-4 governs the -multiplier flag exactly as it does for `run`: this
// file calls the SAME egress.CheckMultiplier `run` uses (buildRunOptions in
// run.go wires the identical flags into internal/run) rather than
// reimplementing the guard. -host-class exists only because replay has no
// torture.yaml / detected compose graph to classify -target from the way
// `run` does; it defaults to "real" — the conservative assumption for
// something replay is, by definition, firing at a live endpoint — so the
// guard is opt-out (-host-class internal) rather than opt-in-by-omission.
func runReplay(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "path to a cassette written by `tortureu capture` (required)")
	target := fs.String("target", "", "base URL to replay the cassette against (required)")
	multiplier := fs.Float64("multiplier", 1, "replay rate multiplier; each cassette entry is sent round(multiplier) times")
	allowRealTraffic := fs.Bool("allow-real-traffic", false, "permit replay above 1x against a class: real host (R-DC2-4)")
	hostClass := fs.String("host-class", "real", "egress class of -target's host for the R-DC2-4 guard: real, mock, internal, or block")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *from == "" {
		fmt.Fprintln(stderr, "tortureu replay: -from is required")
		return 2
	}
	if *target == "" {
		fmt.Fprintln(stderr, "tortureu replay: -target is required")
		return 2
	}

	targetURL, err := url.Parse(*target)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu replay: -target: %v\n", err)
		return 2
	}

	classes := map[string]egress.Class{targetURL.Host: egress.Class(*hostClass)}
	if err := egress.CheckMultiplier(classes, *multiplier, *allowRealTraffic); err != nil {
		fmt.Fprintf(stderr, "tortureu replay: %v\n", err)
		return 2
	}

	f, err := os.Open(*from)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu replay: open %s: %v\n", *from, err)
		return 2
	}
	defer f.Close()

	entries, err := capture.ReadCassette(f)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu replay: %v\n", err)
		return 2
	}
	if len(entries) == 0 {
		fmt.Fprintf(stderr, "tortureu replay: %s has no entries\n", *from)
		return 2
	}

	repeat := int(*multiplier + 0.5)
	res, err := capture.Replay(entries, targetURL, repeat, nil)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu replay: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "sent=%d success=%d failed=%d p50=%.1fms p95=%.1fms p99=%.1fms\n",
		res.Sent, res.Success, res.Failed, res.P50MS, res.P95MS, res.P99MS)
	return 0
}
