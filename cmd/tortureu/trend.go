package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jd316/tortureu/internal/trend"
	"github.com/jd316/tortureu/internal/verdict"
)

// trendUsage is printed for `trend` with no subcommand, or an unknown one. It
// lists what exists rather than guessing at what was meant: `trend shwo` is a
// typo, and running `show` for it would be a different command than the one
// asked for.
const trendUsage = `tortureu trend: local cross-commit trend over recorded verdicts (R-CLI-14).

Usage:
  tortureu trend record <verdict.json>   append one verdict to the store ("-" reads stdin)
  tortureu trend show                    print the series, with per-metric deltas and
                                         findings that appeared or went away

The store is JSONL, one record per line, at ` + trend.DefaultStore + ` by default (-store).
Commit it to track the trend across branches, or gitignore it to keep it local.
`

// runTrend is the `tortureu trend` verb (R-CLI-14..R-CLI-17). It is the local
// half of §12's trend tracking; `tortureu emit bencher` is the remote half,
// and the two are complementary — the same projection idea, one pointed at a
// file, one at Bencher's server.
func runTrend(stdin io.Reader, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, trendUsage)
		return 2
	}
	switch args[0] {
	case "record":
		return trendRecord(stdin, args[1:], stdout, stderr)
	case "show":
		return trendShow(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tortureu trend: unknown subcommand %q\n\n%s", args[0], trendUsage)
		return 2
	}
}

func trendRecord(stdin io.Reader, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trend record", flag.ContinueOnError)
	fs.SetOutput(stderr)
	store := fs.String("store", trend.DefaultStore, "path to the JSONL trend store")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprint(stderr, trendUsage)
		return 2
	}

	src := fs.Arg(0)
	var raw []byte
	var err error
	if src == "-" {
		raw, err = io.ReadAll(stdin)
	} else {
		raw, err = os.ReadFile(src)
	}
	if err != nil {
		fmt.Fprintf(stderr, "tortureu trend record: read verdict: %v\n", err)
		return 2
	}

	var v verdict.Verdict
	if err := json.Unmarshal(raw, &v); err != nil {
		fmt.Fprintf(stderr, "tortureu trend record: %s is not a verdict document (VERDICT.md §1): %v\n", src, err)
		return 2
	}
	if v.Status == "" {
		fmt.Fprintf(stderr, "tortureu trend record: %s has no status; a document with no outcome is not a verdict\n", src)
		return 2
	}

	rec := trend.Project(v)
	if err := trend.Append(*store, rec); err != nil {
		fmt.Fprintf(stderr, "tortureu trend record: %v\n", err)
		return 2
	}

	// R-CLI-17: the run is recorded either way, but the reader is told now —
	// not at `show` time, and not never — that it will never join the series.
	if !rec.Anchored() {
		fmt.Fprintf(stderr, "tortureu trend record: this verdict carries no commit, so it is recorded but\n"+
			"  excluded from the trend. R-VER-12 leaves the anchor empty when the run was not made in a\n"+
			"  git checkout; joining on an empty anchor would collapse every such run onto one point.\n")
	}
	if !rec.Comparable() {
		fmt.Fprintf(stderr, "tortureu trend record: status is %q, which measures the system under test not at\n"+
			"  all (R-VER-2), so the row is shown in the series but contributes no comparison.\n", rec.Status)
	}
	fmt.Fprintf(stdout, "recorded %s (%s) in %s\n", rec.RunID, rec.Status, *store)
	return 0
}

func trendShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trend show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	store := fs.String("store", trend.DefaultStore, "path to the JSONL trend store")
	scenario := fs.String("scenario", "", "only this scenario's series")
	metric := fs.String("metric", "", "only metric keys containing this substring, e.g. p(99)")
	last := fs.Int("n", 0, "show only the last N runs of the series (0 = all)")
	asJSON := fs.Bool("json", false, "print the report as JSON instead of the human rendering")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	s, err := trend.Load(*store)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu trend show: %v\n", err)
		return 2
	}
	rep := s.Report(trend.Filter{Scenario: *scenario, Metric: *metric, Last: *last})

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "tortureu trend show: %v\n", err)
			return 2
		}
	} else {
		// Same report object for both renderings; there is no second
		// formatting path (the R-VER-9 discipline, applied here).
		fmt.Fprint(stdout, trend.Render(rep))
	}
	// A regression is reported, never enforced (R-CLI-14): picking the
	// boundary at which a slower p99 fails a build is a threshold policy, and
	// torture.yaml states none.
	return 0
}
