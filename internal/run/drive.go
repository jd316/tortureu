// drive.go holds the two co-driven load sources registry.yaml registers at
// `drive` tier behind a `run` flag: pgbench (`--db-load`, R-EXE-26) and
// schemathesis (`--fuzz`, R-EXE-27).
//
// `drive` is the whole claim (R-SCOPE-3): both run *while* the HTTP load
// and the faults run, started off k6's own phase clock (R-EXE-8) and torn
// down through the same teardownAll every fault already goes through
// (R-EXE-5, R-EXE-16). Emitting a script for the user to run would be
// `delegate`, and the registry does not say `delegate`.
//
// Both seams are interfaces for the same reason every other seam in this
// package is one: the ordering guarantees (refuse before reset, start on
// the first marker, stop on abort/panic) are provable with fakes, and the
// real implementations (pgbench.go, schemathesis.go) are exercised against
// the actual binaries in drive_real_test.go.
//
// Known limitation, stated rather than hidden (TBD-12): both drive a real
// third-party binary as a subprocess, so neither can use the
// container-network-namespace join K6Runner uses (load.go) or inreach.go's
// fallbackTransport to reach a SUT/database R-DC2-3 put on an internal-only
// network. v0 runs both as host processes against the address the caller
// supplied, and fails loudly when that address is unreachable rather than
// reporting zero DB load or zero fuzz findings.
package run

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/doctor"
	"github.com/jdb316/tortureu/internal/verdict"
)

// DBLoadRunner saturates the database independently of the application
// (R-EXE-26). dsn is the caller-supplied connection string — this package
// never composes one from detected values, because guessing credentials or
// a database name is exactly the guess R-EXE-26 forbids. max bounds the
// load's own lifetime so a runner that outlives this process still stops.
type DBLoadRunner interface {
	Start(dsn string, max time.Duration) (DBLoadHandle, error)
}

// DBLoadHandle is a running DB load. Stop terminates it if it is still
// running and reports what it achieved; it MUST be safe to call more than
// once, since teardownAll can run on several exit paths.
type DBLoadHandle interface {
	Stop() DBLoadResult
}

// DBLoadResult is what a DB load achieved. Err is a *tool* failure — an
// unreachable database, a bad DSN, pgbench itself erroring — which is
// status: error, never a SUT failure (R-VER-2).
type DBLoadResult struct {
	TPS          float64
	Transactions int
	Clients      int
	DurationS    float64
	// CutShort is true when the load ended before the DB load's own
	// duration bound elapsed, i.e. the numbers cover only part of the run.
	CutShort bool
	Err      error
}

// Fuzzer drives a spec-based fuzzer against the SUT while load and faults
// run (R-EXE-27).
type Fuzzer interface {
	Start(specPath, baseURL string, max time.Duration) (FuzzHandle, error)
}

// FuzzHandle is a running fuzz pass. Stop terminates it if it is still
// running and reports what it found; it MUST be safe to call more than once.
type FuzzHandle interface {
	Stop() FuzzResult
}

// FuzzFailure is one operation the fuzzer broke. This is a *result*: the
// system under test returned something its own spec forbids (R-VER-2), so
// it becomes a verdict finding, never a tool error.
type FuzzFailure struct {
	Operation string
	Detail    string
}

// FuzzResult separates the three things a fuzz pass can produce, because
// conflating them is what R-VER-2 forbids: Failures are the SUT breaking,
// Err is the fuzzer being unable to run at all, and Unexecuted counts cases
// that never produced a verdict either way (network errors) — reported as a
// warning so a partially-executed pass is never read as a clean one.
type FuzzResult struct {
	Failures   []FuzzFailure
	Unexecuted int
	CutShort   bool
	Err        error
}

// driveDurationSlack is added to the declared load profile's total duration
// to bound a co-driven tool's own lifetime. The bound is deliberately a
// little longer than the load: the tool is stopped by the run when the load
// finishes, and this only exists so a process that somehow outlives the
// orchestrator still dies by itself (R-EXE-26).
const driveDurationSlack = 30 * time.Second

// driveDurationMin is the floor for that bound, so a very short profile
// (the 1s stages this package's own tests use) still yields a usable
// duration argument.
const driveDurationMin = 10 * time.Second

// driveDuration derives a co-driven tool's upper duration bound from the
// run's own declared load profile (R-CFG-8's `over:`/`for:` per stage).
// This is not a guess: it is the schedule the run itself is going to
// execute.
func driveDuration(cfg *config.Config) time.Duration {
	var total time.Duration
	for _, s := range cfg.Load.Stages {
		spec := s.Over
		if spec == "" {
			spec = s.For
		}
		if d, err := time.ParseDuration(spec); err == nil {
			total += d
		}
	}
	if total+driveDurationSlack < driveDurationMin {
		return driveDurationMin
	}
	return total + driveDurationSlack
}

// checkDriveFlags is every refusal R-EXE-26 and R-EXE-27 require, in one
// place, called before reset and before any load starts. Each returns a
// message naming what was absent and what would supply it: this project
// treats a silent no-op as its worst failure mode, and a flag that quietly
// did nothing is exactly that.
func checkDriveFlags(cfg *config.Config, sys detect.System, deps Deps, opts Options) error {
	if opts.DBLoad {
		if !hasDepType(sys, "postgresql") {
			return fmt.Errorf("--db-load drives pgbench against a postgresql dependency (registry: pgbench, when: dep:postgresql), and detection found none in %s — refusing rather than silently skipping the DB load (R-EXE-26)", cfg.Target.Compose)
		}
		if strings.TrimSpace(opts.DBURL) == "" {
			return fmt.Errorf("--db-load needs a connection string: pass -db-url. TortureU never guesses a user, password, host, port or database name (R-EXE-26)")
		}
		if deps.DBLoad == nil {
			return fmt.Errorf("--db-load was requested but no DB load runner is wired — refusing rather than reporting a run that never touched the database (R-EXE-26)")
		}
		if err := drivePreflight(deps.DBLoad); err != nil {
			return err
		}
	}
	if opts.Fuzz {
		if !sys.Coverage.OpenAPI {
			return fmt.Errorf("--fuzz drives schemathesis against an OpenAPI document (registry: schemathesis, when: spec:openapi), and detection reports spec:openapi false — refusing rather than silently skipping the fuzz pass (R-EXE-27)")
		}
		if fuzzSpecPath(cfg, opts) == "" {
			return fmt.Errorf("--fuzz needs the OpenAPI document to fuzz: set target.openapi in torture.yaml or pass -fuzz-spec. TortureU does not guess it by scanning for conventional filenames (R-EXE-27)")
		}
		if strings.TrimSpace(cfg.Target.BaseURL) == "" {
			return fmt.Errorf("--fuzz needs a URL to fuzz: set target.base_url in torture.yaml (R-EXE-27)")
		}
		if deps.Fuzz == nil {
			return fmt.Errorf("--fuzz was requested but no fuzzer is wired — refusing rather than reporting a run that never fuzzed anything (R-EXE-27)")
		}
		if err := drivePreflight(deps.Fuzz); err != nil {
			return err
		}
	}
	return nil
}

// drivePreflight asks a runner whether it can run at all, if it knows how
// to answer. Duck-typed for the same reason EnsureProxies/SetSUTContainer
// are (run.go): every fake in this package's tests stays unaffected, and
// only the real runners implement it. Its job is R-CLI-5's: a missing
// binary is reported with an install hint, never as an obscure failure
// three steps later.
func drivePreflight(v any) error {
	p, ok := v.(interface{ Preflight() error })
	if !ok {
		return nil
	}
	return p.Preflight()
}

func hasDepType(sys detect.System, typ string) bool {
	for _, d := range sys.Deps {
		if d.Type == typ {
			return true
		}
	}
	return false
}

// fuzzSpecPath is the one place the fuzzed document's path is decided:
// -fuzz-spec wins over torture.yaml's target.openapi, and "" means neither
// was supplied (which checkDriveFlags turns into a refusal, never a guess).
func fuzzSpecPath(cfg *config.Config, opts Options) string {
	if s := strings.TrimSpace(opts.FuzzSpec); s != "" {
		return s
	}
	return strings.TrimSpace(cfg.Target.OpenAPI)
}

// coDrivers owns the lifetime of the co-driven tools for one run: started
// on k6's first phase marker, stopped exactly once by whichever path gets
// there first (load end, abort, signal, panic).
type coDrivers struct {
	cfg  *config.Config
	deps Deps
	opts Options

	startOnce sync.Once
	stopOnce  sync.Once

	mu     sync.Mutex
	db     DBLoadHandle
	fuzz   FuzzHandle
	errs   []error
	result driveResults
	// started records whether the first marker ever arrived. A load that
	// emitted no phase at all means the co-driven tools never ran, which is
	// reported, never assumed away.
	started bool
}

// driveResults is what the co-driven tools produced, collected once.
type driveResults struct {
	db      *DBLoadResult
	fuzz    *FuzzResult
	dbErr   error
	fuzzErr error
}

func newCoDrivers(cfg *config.Config, deps Deps, opts Options) *coDrivers {
	return &coDrivers{cfg: cfg, deps: deps, opts: opts}
}

// start launches the co-driven tools. Called (at most once) from the marker
// tee when k6 announces its first phase — that is the binding to the run
// clock R-EXE-26/R-EXE-27 require.
func (c *coDrivers) start() {
	c.startOnce.Do(func() {
		max := driveDuration(c.cfg)
		c.mu.Lock()
		c.started = true
		c.mu.Unlock()

		if c.opts.DBLoad {
			h, err := c.deps.DBLoad.Start(c.opts.DBURL, max)
			c.mu.Lock()
			c.db, c.result.dbErr = h, err
			c.mu.Unlock()
		}
		if c.opts.Fuzz {
			h, err := c.deps.Fuzz.Start(fuzzSpecPath(c.cfg, c.opts), c.cfg.Target.BaseURL, max)
			c.mu.Lock()
			c.fuzz, c.result.fuzzErr = h, err
			c.mu.Unlock()
		}
	})
}

// stop terminates both tools and records their results. Idempotent: it runs
// from teardownAll (every abort/panic/signal path) and from Run's own
// post-load collection, whichever reaches it first.
func (c *coDrivers) stop() {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		db, fz := c.db, c.fuzz
		c.mu.Unlock()

		var dbRes *DBLoadResult
		var fzRes *FuzzResult
		if db != nil {
			r := db.Stop()
			dbRes = &r
		}
		if fz != nil {
			r := fz.Stop()
			fzRes = &r
		}

		c.mu.Lock()
		c.result.db, c.result.fuzz = dbRes, fzRes
		c.mu.Unlock()
	})
}

// collect stops the tools (if not already stopped) and returns what they
// produced.
func (c *coDrivers) collect() driveResults {
	c.stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result
}

func (c *coDrivers) everStarted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

// teeMarkers forwards k6's phase markers unchanged to the fault scheduler
// while calling onFirst once, when the first one arrives. The callback runs
// in its own goroutine deliberately: starting pgbench includes an
// initialization step that takes real time, and blocking the marker stream
// on it would delay every phase-anchored fault — the one thing this
// package's single-clock guarantee (R-EXE-1) may not do.
func teeMarkers(in <-chan PhaseMarker, onFirst func()) <-chan PhaseMarker {
	out := make(chan PhaseMarker, 8)
	go func() {
		defer close(out)
		var once sync.Once
		for m := range in {
			once.Do(func() { go onFirst() })
			out <- m
		}
	}()
	return out
}

// applyDriveResults folds the co-driven tools' output into the verdict:
// the DB load as an artifact, fuzz failures as findings, everything
// partial or unexecuted as a warning. It returns a tool error (R-VER-2's
// `error`) when a tool could not run at all — never for a finding, which
// is the SUT failing, not TortureU.
func applyDriveResults(v *verdict.Verdict, res driveResults, cfg *config.Config, sys detect.System, auditFindings []doctor.Finding, everStarted bool) ([]verdict.Finding, error) {
	if !everStarted {
		addWarning(v, "co-driven load sources (--db-load/--fuzz) never started: the load generator announced no phase, so there was no run clock to start them on (R-EXE-8)")
		return nil, nil
	}

	if res.dbErr != nil {
		return nil, fmt.Errorf("--db-load: %w", res.dbErr)
	}
	if res.db != nil {
		if res.db.Err != nil {
			return nil, fmt.Errorf("--db-load: %w", res.db.Err)
		}
		if v.Artifacts == nil {
			v.Artifacts = map[string]any{}
		}
		v.Artifacts["db_load"] = map[string]any{
			"tool":         "pgbench",
			"tps":          res.db.TPS,
			"transactions": res.db.Transactions,
			"clients":      res.db.Clients,
			"duration_s":   res.db.DurationS,
			"cut_short":    res.db.CutShort,
		}
		if res.db.CutShort {
			addWarning(v, "--db-load: pgbench was cut short when the load ended, so its tps covers only part of the run")
		}
	}

	if res.fuzzErr != nil {
		return nil, fmt.Errorf("--fuzz: %w", res.fuzzErr)
	}
	if res.fuzz == nil {
		return nil, nil
	}
	if res.fuzz.Err != nil {
		return nil, fmt.Errorf("--fuzz: %w", res.fuzz.Err)
	}
	if res.fuzz.CutShort {
		addWarning(v, "--fuzz: the fuzz pass was cut short when the load ended — it reports only what it had found by then, not a clean bill of health")
	}
	if res.fuzz.Unexecuted > 0 {
		addWarning(v, fmt.Sprintf("--fuzz: %d case(s) could not be executed at all (network errors), so they are neither a pass nor a finding", res.fuzz.Unexecuted))
	}

	// R-VER-3: with no fault declared, the fuzzer's own request is the sole
	// candidate cause and schemathesis reports it verbatim, so the finding
	// is `correlated`. With a fault in the run, the injected fault is a
	// second candidate cause and there are no traces to separate them, so
	// it degrades to `ambiguous`.
	conf := verdict.Correlated
	if len(cfg.Faults) > 0 {
		conf = verdict.Ambiguous
	}
	// R-VER-4: a fuzz finding has no fault to attribute, so candidates come
	// from the same no-fault path the rest of this package uses
	// (findings.go), never from an invented attribution.
	candidates := buildCandidatesFromDetectedDeps(sys.Deps, auditFindings, sys.Lang)

	var findings []verdict.Finding
	for _, f := range res.fuzz.Failures {
		findings = append(findings, verdict.Finding{
			Confidence: conf,
			Broke: verdict.Broke{
				Assertion: "schemathesis: " + f.Operation,
				Observed:  f.Detail,
			},
			Candidates: candidates,
		})
	}
	return findings, nil
}
