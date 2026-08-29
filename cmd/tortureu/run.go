package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/detect"
	tortureurun "github.com/jd316/TortureU/internal/run"
	"github.com/jd316/TortureU/internal/trend"
	"github.com/jd316/TortureU/internal/verdict"
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
	recordTrend := fs.Bool("trend", false, "append this verdict to the trend store ("+trend.DefaultStore+"); off by default (R-CLI-18)")
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
	sqlURL := fs.String("sql-url", "", "database URL for sql: asserts (R-CFG-18); they are reported unevaluated when empty")
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
	// The two drive-tier co-executed load sources (R-EXE-26, R-EXE-27).
	// Neither carries a default address or spec path: a missing -db-url or
	// target.openapi is a loud refusal, never a guess (see internal/run's
	// checkDriveFlags). -db-load's help states the pgbench_* write because
	// it is a write against the caller's own database.
	dbLoad := fs.Bool("db-load", false, "co-execute pgbench against the detected postgresql dependency for the run's duration (R-EXE-26); requires -db-url. NOTE: pgbench initialization CREATES AND DROPS tables named pgbench_* in that database")
	dbURL := fs.String("db-url", "", "PostgreSQL connection string for -db-load, e.g. postgresql://user:pass@host:5432/db; never guessed from the compose file")
	fuzz := fs.Bool("fuzz", false, "co-execute schemathesis against the SUT's OpenAPI document for the run's duration (R-EXE-27); requires spec:openapi and target.openapi (or -fuzz-spec)")
	fuzzSpec := fs.String("fuzz-spec", "", "OpenAPI document for -fuzz; defaults to torture.yaml's target.openapi, and is never guessed by scanning filenames")
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

	// R-DET-19: target.service is authoritative and already validated, so
	// detection must not be left to re-derive it. On a stack with several
	// candidate build: services R-DET-19 correctly names none, and an empty
	// sys.SUT would silently degrade two things that key off it — the
	// audit-candidate join (R-VER-4) and the Jaeger service lookup
	// (R-VER-13) — both of which fail closed rather than loudly.
	sys, err := detect.DetectWithSUT(cfg.Target.Compose, cfg.Target.Service)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu run: detect: %v\n", err)
		return 2
	}

	// R-CFG-18: a bad -sql-url must fail here, loudly. Downgrading it to a
	// nil querier would report the asserts as "unevaluated" — the same
	// wording a user gets for supplying no URL at all — and so hide the
	// fact that they asked for evaluation and did not get it.
	var sqlQuerier tortureurun.SQLQuerier
	if *sqlURL != "" {
		q, qerr := tortureurun.NewSQLQuerier(*sqlURL)
		if qerr != nil {
			fmt.Fprintf(stderr, "tortureu run: %v\n", qerr)
			return 2
		}
		sqlQuerier = q
	}

	deps := buildRealDeps(*toxiproxyURL, *promURL, *mockURL, *brokerURL, sqlQuerier)
	opts := buildRunOptions(*noReset, *allowRealTraffic, *multiplier, driveFlags{
		dbLoad: *dbLoad, dbURL: *dbURL, fuzz: *fuzz, fuzzSpec: *fuzzSpec,
	})
	v := tortureurun.Run(cfg, *sys, deps, opts)
	// R-CLI-18: bookkeeping never changes what the experiment found, so a
	// store that cannot be written is reported and nothing else — the exit
	// code stays the verdict's.
	if *recordTrend {
		if terr := trend.Append("", trend.Project(*v)); terr != nil {
			fmt.Fprintf(stderr, "tortureu run: --trend: %v\n", terr)
		}
	}

	return emitVerdict(v, *asJSON, stdout)
}

// buildRunOptions is the one call site that turns the run verb's flags into
// internal/run.Options. It exists as its own function, the same reasoning
// as buildRealDeps: a test must be able to prove -allow-real-traffic and
// -multiplier actually reach Options (R-DC2-4) instead of the wiring being
// asserted only by reading the source — this is the fourth time in this
// project a value existed and never reached its consumer.
func buildRunOptions(noReset, allowRealTraffic bool, multiplier float64, drive driveFlags) tortureurun.Options {
	return tortureurun.Options{
		NoReset:          noReset,
		AllowRealTraffic: allowRealTraffic,
		Multiplier:       multiplier,
		DBLoad:           drive.dbLoad,
		DBURL:            drive.dbURL,
		Fuzz:             drive.fuzz,
		FuzzSpec:         drive.fuzzSpec,
	}
}

// driveFlags groups the four drive-tier co-execution flags (R-EXE-26,
// R-EXE-27) so buildRunOptions keeps one argument per concern rather than
// eight positional bools and strings.
type driveFlags struct {
	dbLoad   bool
	dbURL    string
	fuzz     bool
	fuzzSpec string
}

// buildRealDeps is the one call site that wires the four `run` endpoint
// flags into internal/run's real, live-infra Deps. It exists as its own
// function — rather than inlined at runRun's call site — so a test can
// prove the flags actually reach NewRealDepsFull (R-EXE-19) instead of the
// wiring being asserted only by reading the source: passing -mock-url or
// -broker-url must be observable as Deps.MockApplier / Deps.QueueApplier
// going from nil to non-nil, the same way internal/run's own tests observe
// NewRealDepsFull.
func buildRealDeps(toxiproxyURL, promURL, mockURL, brokerURL string, sql tortureurun.SQLQuerier) tortureurun.Deps {
	return tortureurun.NewRealDepsFull(toxiproxyURL, promURL, mockURL, brokerURL, sql)
}
