// Package run is the orchestrator: the one place that drives load and
// faults off a single shared clock (R-SCOPE-2, R-EXE-1) and enforces the
// ordering that makes every other package's guarantee real instead of
// generated-but-unapplied (R-DC2-7).
//
// Every dependency this package needs from the rest of the system is a
// narrow interface (Resetter, TopologyApplier, LoadRunner, PromQuerier,
// fault.Applier). Run itself is pure orchestration over those seams, so the
// ordering guarantees — abort before load, teardown on panic or signal,
// egress enforced before the first request — are provable with fakes and
// need no live Docker daemon. The concrete implementations of those seams
// (docker_applier.go, toxiproxy_applier.go, promql.go, load.go, topology.go,
// reset.go) are real, and are exercised against a live Docker daemon or an
// httptest fake standing in for a real HTTP contract where a live daemon
// for that specific dependency (Toxiproxy, Prometheus, k6) is not available
// in this environment.
package run

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	realapplier "github.com/jdb316/tortureu/internal/applier"
	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/doctor"
	"github.com/jdb316/tortureu/internal/egress"
	"github.com/jdb316/tortureu/internal/fault"
	"github.com/jdb316/tortureu/internal/k6"
	"github.com/jdb316/tortureu/internal/queuefault"
	"github.com/jdb316/tortureu/internal/verdict"
)

// Options carries the run-level flags that live outside torture.yaml
// (R-CFG-20, R-DC2-4).
type Options struct {
	// NoReset skips the reset step (R-CFG-20's --no-reset).
	NoReset bool
	// AllowRealTraffic opts into replay above 1x against a class: real host
	// (R-DC2-4).
	AllowRealTraffic bool
	// Multiplier is the replay multiplier CheckMultiplier guards. Zero is
	// treated as 1x (no replay scaling), never as "no traffic".
	Multiplier float64
	// DBLoad co-executes pgbench against the detected PostgreSQL
	// dependency for the duration of the run (R-EXE-26's --db-load).
	DBLoad bool
	// DBURL is the connection string pgbench is given. There is no default
	// and none is derived: R-EXE-26 forbids guessing credentials or a
	// database name.
	DBURL string
	// Fuzz co-executes schemathesis against the SUT's OpenAPI document for
	// the duration of the run (R-EXE-27's --fuzz).
	Fuzz bool
	// FuzzSpec overrides torture.yaml's target.openapi as the document to
	// fuzz. Empty with no target.openapi is a refusal, never a search.
	FuzzSpec string
}

// Resetter runs the user-supplied reset command before load begins
// (R-CFG-20, R-CFG-21, R-EXE-2).
type Resetter interface {
	Reset(command string) error
}

// PhaseMarker is one stage-transition event read off k6's own execution
// clock (R-EXE-8) — the "single shared clock" R-SCOPE-2/R-EXE-1 require.
type PhaseMarker struct {
	Phase string
	At    time.Time
}

// LoadResult is what a completed load run produced.
type LoadResult struct {
	// SummaryJSON is k6's machine-readable summary (R-VER-10); internal/k6's
	// IngestSummary is the only thing that may parse it.
	SummaryJSON []byte
}

// LoadHandle is a running load generator: its Markers channel is the clock
// the fault scheduler follows (R-EXE-8), its Done channel yields the final
// summary, and Err reports a load-generator failure distinct from the
// system under test failing an assertion (R-VER-2).
type LoadHandle interface {
	Markers() <-chan PhaseMarker
	Done() <-chan LoadResult
	Err() <-chan error
}

// LoadRunner starts the compiled k6 script (internal/k6.Compile's output)
// and returns a handle to its clock.
type LoadRunner interface {
	Start(script string) (LoadHandle, error)
}

// TopologyApplier applies the R-DC2-3 egress overlay against the SUT's
// compose stack so enforcement is structural (Docker's network stack), not
// a policy check TortureU could get wrong. Apply MUST return before the
// first request is generated (R-EXE-3). externalHosts are the classified
// mock/real "host:port" egress keys that need a DNS path to the proxy;
// internalHosts are class: internal fault targets needing the R-EXE-20
// rename-and-alias treatment. Both are documented on
// ComposeTopologyApplier.Apply.
type TopologyApplier interface {
	Apply(composePath string, top egress.Topology, externalHosts, internalHosts []string) error
}

// PromQuerier evaluates one promql: expression over the run window
// (R-CFG-17) — the signals k6 cannot observe. holds reports whether the
// expression's condition held (Prometheus's own filter semantics: a
// comparison expression like `x < 100` returns a non-empty result exactly
// when the condition holds); observed is a human-readable rendering of what
// was measured, for the verdict's `passed`/`broke.observed` fields.
type PromQuerier interface {
	Query(expr string) (holds bool, observed string, err error)
}

// MockApplier is the mock-provider owner R-EXE-15 names for error_rate:
// internal/applier.WireMockApplier's ApplyErrorRate, narrowed to an
// interface so tests can fake it without a live WireMock instance.
type MockApplier interface {
	ApplyErrorRate(faultName string, r realapplier.ErrorRate) (func() error, realapplier.ErrorRateApplied, error)
}

// Deps is every external seam Run needs. Production callers get one from
// NewRealDeps (docker_applier.go, toxiproxy_applier.go, promql.go, load.go,
// topology.go, reset.go); tests substitute fakes for some or all fields.
type Deps struct {
	Reset    Resetter
	Topology TopologyApplier
	Load     LoadRunner
	Applier  fault.Applier
	// QueueApplier is internal/queuefault's real broker client
	// (internal/applier.BrokerApplier), the owner R-EXE-15 names for
	// poison_pill/duplicate. nil means none is wired: a run declaring
	// either verb then fails per R-EXE-19 rather than silently skipping
	// the fault.
	QueueApplier queuefault.Applier
	// MockApplier is internal/applier.WireMockApplier, the owner R-EXE-15
	// names for error_rate. nil means none is wired: a run declaring
	// error_rate against a class: mock host then fails per R-EXE-19 rather
	// than silently skipping the fault.
	MockApplier MockApplier
	// DBLoad is the drive-tier DB saturation runner (PgbenchRunner,
	// pgbench.go), used only when Options.DBLoad is set. nil with the flag
	// set is a refusal, never a silent skip (R-EXE-26).
	DBLoad DBLoadRunner
	// Fuzz is the drive-tier spec fuzzer (SchemathesisRunner,
	// schemathesis.go), used only when Options.Fuzz is set. nil with the
	// flag set is a refusal, never a silent skip (R-EXE-27).
	Fuzz Fuzzer
	Prom PromQuerier
	// Now is the wall clock used only for verdict timestamps/duration —
	// never for fault scheduling (R-EXE-8: that clock is k6's). Defaults to
	// time.Now.
	Now func() time.Time
}

// Naming for the R-DC2-3 topology overlay. SPEC.md does not name the
// TortureU proxy service or the internal/egress networks it wires
// (escalated in the Task 7 report); these are this package's own fixed
// convention, not user-configurable in v0.
const (
	sutNetworkName    = "tortureu_sut"
	egressNetworkName = "tortureu_egress"
	proxyServiceName  = "tortureu-proxy"
)

func (o Options) multiplier() float64 {
	if o.Multiplier <= 0 {
		return 1
	}
	return o.Multiplier
}

func randSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newVerdict(now time.Time, scenario string) *verdict.Verdict {
	return &verdict.Verdict{
		RunID:     fmt.Sprintf("%s-%s-%s", now.UTC().Format("2006-01-02T15:04:05Z"), scenario, randSuffix()),
		Scenario:  scenario,
		Status:    verdict.StatusError,
		StartedAt: now.UTC().Format(time.RFC3339),
		Reset:     "clean",
	}
}

// Run executes one torture.yaml scenario end to end: reset, abort-before-
// load egress classification, topology enforcement, load+faults on one
// clock, promql/k6-threshold evaluation, guaranteed teardown, one verdict
// (R-EXE-1..9, R-VER-1).
//
// Run never itself calls os.Exit; the caller derives the process exit code
// from verdict.ExitCode(*Run(...)) per R-VER-7.
func Run(cfg *config.Config, sys detect.System, deps Deps, opts Options) (runVerdict *verdict.Verdict) {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	started := now()
	v := newVerdict(started, cfg.Target.Service)
	// R-VER-11: state what this repo's observability can support, on every
	// verdict including the ones that end early. Detection has already
	// computed it (R-DET-6) and nothing here recomputes or second-guesses
	// it; MaxConfidence has a floor of "correlated" and is never empty.
	// R-VER-12: the commit this run is anchored to, resolved once, on the
	// same path as Observability so early exits carry it too. Empty when
	// this is not a git checkout — never a placeholder.
	v.Commit = commitAnchor(cfg.Target.Compose)

	v.Observability = verdict.Observability{
		Traces:        sys.Obs.Traces,
		Metrics:       sys.Obs.Metrics,
		Logs:          sys.Obs.Logs,
		MaxConfidence: verdict.Confidence(sys.Obs.MaxConfidence),
	}
	finish := func(status verdict.Status) *verdict.Verdict {
		v.Status = status
		v.DurationS = int(now().Sub(started).Seconds())
		return v
	}
	// fail sets v.Error to an actionable reason and finishes as
	// status:error (R-VER-2: "fail" means the SUT broke, "error" means the
	// tool broke, and a status:error with no reason is indistinguishable
	// from a shrug — a user cannot tell "go debug your service" from "go
	// install k6" from status alone). reason names what failed; err is
	// always appended, never replaced by reason alone, since the detail
	// matters when the cause isn't one this package anticipated.
	fail := func(reason string, err error) *verdict.Verdict {
		v.Error = fmt.Sprintf("%s: %v", reason, err)
		return finish(verdict.StatusError)
	}

	manager := &fault.Manager{}

	// teardownExpiring is reassigned once scheduleFaults starts (it tears
	// down the for:-duration faults applied outside fault.Manager, see
	// scheduler.go). It starts as a no-op (nothing to tear down yet) and is
	// read/written through a mutex because both the signal watcher below and
	// Run's own main flow can reach it concurrently, from a point in the
	// run's lifetime that isn't known in advance.
	var teMu sync.Mutex
	teardownExpiring := func() {}
	setTeardownExpiring := func(f func()) {
		teMu.Lock()
		teardownExpiring = f
		teMu.Unlock()
	}
	// teardownTopology is reassigned once Topology.Apply succeeds (below),
	// the same lazily-armed pattern as teardownExpiring: nothing to tear
	// down before Apply runs, so it starts as a no-op. It removes any
	// service R-EXE-20's rename trick disabled via an unused compose
	// profile — an E1 finding showed `docker compose down` (with or without
	// this package's own overlay) does not reliably remove a profile-gated
	// service that was already running from an earlier `up`, leaking a
	// still-running, still-port-bound container into the next run. That is
	// the exact "a failed run poisons the next one" failure class this
	// package already fixed once for its own test suite (see
	// ProxyControlPort's doc comment) reappearing through a different
	// mechanism for real `tortureu run` users, so it goes through the same
	// teardownAll every other cleanup obligation already does — every exit
	// path, not just the success one (R-EXE-5).
	var topoMu sync.Mutex
	teardownTopology := func() {}
	setTeardownTopology := func(f func()) {
		topoMu.Lock()
		teardownTopology = f
		topoMu.Unlock()
	}
	// teardownDrivers is the same lazily-armed pattern as the two above,
	// for the co-driven load sources (--db-load / --fuzz, R-EXE-26/27):
	// nothing to stop until the first phase marker starts them. A crashed
	// or interrupted run must never leave pgbench hammering a developer's
	// database (R-EXE-5, R-EXE-16), so it hangs off the same teardownAll
	// every fault already does.
	var drvMu sync.Mutex
	teardownDrivers := func() {}
	setTeardownDrivers := func(f func()) {
		drvMu.Lock()
		teardownDrivers = f
		drvMu.Unlock()
	}
	teardownAll := func() {
		manager.Teardown() // R-EXE-5
		drvMu.Lock()
		df := teardownDrivers
		drvMu.Unlock()
		df()
		teMu.Lock()
		f := teardownExpiring
		teMu.Unlock()
		f()
		topoMu.Lock()
		tf := teardownTopology
		topoMu.Unlock()
		tf()
		// A pooled resource is easier to leak than a per-request one
		// (inreach.go's hopTransports doc comment): every cached
		// container-network tunnel closes here, on every exit path, not
		// just a clean finish.
		closeHopTransports()
	}

	// R-EXE-16: a signal must tear everything down no matter when it
	// arrives — before load starts, mid-load, or in the window between the
	// load finishing and the scheduler's own goroutines draining. This
	// package used to call fault.Manager.WatchSignals for this, but that
	// only ever knew about fault.Manager's own tracked faults; a signal in
	// most windows never reached teardownExpiring (the for:-duration faults
	// tracked outside Manager, see scheduler.go), silently leaving them
	// applied. fault.Manager.WatchSignals is not called anywhere in this
	// file any more — this package now arms its own signal.Notify for
	// Run's entire body instead, precisely so there is no such window.
	var aborted atomic.Bool
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	quit := make(chan struct{})
	caught := make(chan os.Signal, 1)
	go func() {
		select {
		case sig := <-sigCh:
			teardownAll()
			aborted.Store(true)
			caught <- sig
		case <-quit:
		}
	}()
	defer func() {
		signal.Stop(sigCh)
		close(quit)
	}()

	defer func() {
		if r := recover(); r != nil {
			// A panic unwinds past every `return finish(...)` call site, so
			// without a named return value this would hand the caller nil
			// instead of a verdict — Run has one (result) specifically so
			// this assignment actually reaches the caller.
			teardownAll() // R-EXE-5: teardown on panic
			v.Status = verdict.StatusError
			v.Error = fmt.Sprintf("panic: %v", r) // R-VER-2: never a bare, reasonless error
			v.DurationS = int(now().Sub(started).Seconds())
			runVerdict = v
		}
	}()

	// abortedEarly returns the aborted verdict if the signal watcher already
	// fired (and already tore everything down) by the time a synchronous
	// step below finishes; called after every step through which a signal
	// might have arrived while this goroutine was blocked elsewhere.
	abortedEarly := func() (*verdict.Verdict, bool) {
		if aborted.Load() {
			return finish(verdict.StatusAborted), true
		}
		return nil, false
	}

	// 0. Refuse an unusable drive-tier flag before anything is perturbed
	// (R-EXE-26, R-EXE-27): no postgres to saturate, no connection string,
	// no OpenAPI document, no binary on PATH. Discovering any of those
	// after a reset has already wiped the stack teaches the user nothing,
	// and silently proceeding without the co-driven load would be the
	// silent omission this project rejects everywhere.
	if err := checkDriveFlags(cfg, sys, deps, opts); err != nil {
		return fail("drive-tier co-execution refused", err)
	}

	// 1. Reset (R-CFG-20/21, R-EXE-2), unless --no-reset.
	if opts.NoReset {
		v.Reset = "skipped"
	} else if err := deps.Reset.Reset(cfg.Reset.Command); err != nil {
		v.Reset = "failed"
		return finish(verdict.StatusAborted)
	}
	if av, ok := abortedEarly(); ok {
		return av
	}

	// 2. Classify egress; abort before any load starts (R-DC2-2).
	classes := egress.Classify(sys.EgressClass, cfg.Egress)
	v.EgressAudit = egress.Audit(classes)
	if err := egress.CheckUnclassified(classes); err != nil {
		return finish(verdict.StatusAborted)
	}
	if err := egress.CheckMultiplier(classes, opts.multiplier(), opts.AllowRealTraffic); err != nil {
		return finish(verdict.StatusAborted)
	}
	if av, ok := abortedEarly(); ok {
		return av
	}

	// 3. Apply the topology overlay so egress is enforced (R-DC2-3, R-EXE-3)
	// before load starts generating requests. externalHosts (class: mock or
	// real) need a proxy reachable *before* any fault ever targets them —
	// a host with no fault at all must still never be reachable directly
	// (R-DC2-2's guarantee holds for the whole run, not just fault windows).
	// internalHosts (R-EXE-20) are class: internal fault targets with a
	// network-verb fault attached: pg_slow/redis_dies-shaped faults, the
	// product's primary case, not an edge case (SPEC.md's own words).
	top := egress.BuildTopology(sutNetworkName, egressNetworkName, proxyServiceName)
	var externalHosts []string
	for host, class := range classes {
		if class == egress.ClassReal || class == egress.ClassMock {
			externalHosts = append(externalHosts, host)
		}
	}
	internalHosts := internalNetworkFaultTargets(cfg.Faults, classes)
	if err := deps.Topology.Apply(cfg.Target.Compose, top, externalHosts, internalHosts); err != nil {
		teardownAll()
		return fail("apply egress topology", err)
	}
	// Apply succeeded: arm the disabled-service cleanup for every remaining
	// exit path (see teardownTopology's doc comment above). Duck-typed
	// (like EnsureProxies/SetSUTContainer below) so fakeTopology-based
	// tests — the overwhelming majority — are unaffected; only
	// ComposeTopologyApplier implements it.
	if td, ok := deps.Topology.(interface {
		TeardownDisabled(composePath string, internalHosts []string) error
	}); ok {
		setTeardownTopology(func() {
			if err := td.TeardownDisabled(cfg.Target.Compose, internalHosts); err != nil {
				addWarning(v, fmt.Sprintf("teardown of disabled dependency container(s) failed: %v", err))
			}
		})
	}
	if preparer, ok := deps.Applier.(interface{ EnsureProxies(map[string]string) error }); ok {
		targets := map[string]string{}
		for _, h := range externalHosts {
			targets[h] = h
		}
		for _, h := range internalHosts {
			if _, port, err := net.SplitHostPort(h); err == nil {
				targets[h] = backendServiceName(hostnameOf(h)) + ":" + port
			}
		}
		if err := preparer.EnsureProxies(targets); err != nil {
			teardownAll()
			return fail("configure egress proxy", err)
		}
	}

	// R-DC2-3's own network puts the SUT where the host cannot reach its
	// published ports at all (Docker does not publish ports for a
	// container whose only network is internal — the eval that found this
	// confirmed it independently of any fixture). A LoadRunner that
	// supports SetSUTContainer (K6Runner does) gets the SUT's actual
	// container name so it can run k6 sharing that container's network
	// namespace instead of dialing a published port that doesn't exist
	// (see load.go's package doc comment for the full mechanism). Discovery
	// only runs at all when the LoadRunner asks for it — every test using
	// a fake LoadRunner (the overwhelming majority) never shells out to
	// docker. If it asks and discovery fails, the run fails loudly here
	// rather than silently falling back to the host-process path this
	// task's eval proved is always connection-refused against an
	// internal-only SUT — a fallback would just reintroduce the bug quietly.
	if setter, ok := deps.Load.(interface{ SetSUTContainer(string) }); ok {
		sutContainer, err := discoverSUTContainer(cfg.Target.Service)
		if err != nil {
			teardownAll()
			return fail("locate SUT container for the load generator's network attachment", err)
		}
		setter.SetSUTContainer(sutContainer)
	}

	// R-CFG-17/R-EXE-15: the orchestrator itself makes outbound calls into
	// the stack DC-2 just isolated — promql: asserts against Prometheus,
	// poison_pill/duplicate against the broker's admin API — and an E1
	// finding showed neither has any equivalent of the fix above: a plain
	// host-process HTTP call cannot reach a target R-DC2-3 moved onto the
	// SUT's internal-only network, the identical reachability problem
	// solved for k6/the SUT, one layer over. HTTPPromQuerier's default
	// client (promql.go) already carries this fix internally; BrokerApplier
	// lives in internal/applier (untouched here) but already exposes an
	// injectable Client field for exactly this kind of substitution — wire
	// the same fallback transport into it, without overriding a caller who
	// already supplied their own client.
	if broker, ok := deps.QueueApplier.(*realapplier.BrokerApplier); ok && broker.Client == nil {
		broker.Client = &http.Client{Transport: fallbackTransport{}}
	}

	script, err := k6.Compile(cfg)
	if err != nil {
		teardownAll()
		return fail("compile k6 script", err)
	}
	if av, ok := abortedEarly(); ok {
		return av
	}

	// 4. Start load; schedule faults against k6's phase markers (R-EXE-8).
	handle, err := deps.Load.Start(script)
	if err != nil {
		// The most likely first-run failure of all: we do not bundle k6
		// (R-LIC-1), so "k6 not on PATH" is what most new users will hit
		// first. K6Runner already turns that into an actionable message
		// (wrapStartError, load.go); other LoadRunner implementations'
		// errors pass through as-is, still wrapped with what step failed.
		teardownAll()
		return fail("start load generator", err)
	}
	// R-EXE-26/R-EXE-27: the co-driven tools start off the *same* clock the
	// faults follow — k6's first phase marker (R-EXE-8) — not off this
	// package's wall clock, and not before load exists. teeMarkers forwards
	// every marker to the scheduler unchanged, so the fault path is
	// untouched whether or not either flag is set.
	markers := handle.Markers()
	var drivers *coDrivers
	if opts.DBLoad || opts.Fuzz {
		drivers = newCoDrivers(cfg, deps, opts)
		markers = teeMarkers(markers, drivers.start)
		setTeardownDrivers(drivers.stop)
	}
	schedDone, expiringTeardown := scheduleFaults(cfg.Faults, markers, started, manager, deps.Applier, deps.QueueApplier, deps.MockApplier, classes)
	setTeardownExpiring(expiringTeardown)

	var result LoadResult
	select {
	case result = <-handle.Done():
	case <-caught:
		// teardownAll already ran inside the signal watcher goroutine.
		return finish(verdict.StatusAborted)
	case loadErr := <-handle.Err():
		teardownAll()
		return fail("load generator failed", loadErr)
	}
	if av, ok := abortedEarly(); ok {
		return av
	}
	// R-EXE-19: a fault that couldn't be routed to a wired owner fails the
	// run instead of silently proceeding as if it had been applied.
	// approximationNote entries (R-EXE-22) are not failures — split them
	// out and surface them as verdict warnings instead of failing the run
	// over a rate that was merely rounded, not unrouted.
	if schedErrs := <-schedDone; len(schedErrs) > 0 {
		var realErrs []error
		for _, e := range schedErrs {
			if note, ok := e.(approximationNote); ok {
				addWarning(v, note.msg)
				continue
			}
			realErrs = append(realErrs, e)
		}
		if len(realErrs) > 0 {
			teardownAll()
			return fail("fault scheduling failed", errors.Join(realErrs...))
		}
	}
	if av, ok := abortedEarly(); ok {
		return av
	}

	metrics, err := k6.IngestSummary(result.SummaryJSON)
	if err != nil {
		teardownAll()
		return fail("parse k6 summary", err)
	}
	v.Metrics = metrics

	// R-DET-1 forbids detection itself from reading source; R-AUD-5
	// explicitly permits internal/doctor's own bounded, table-driven
	// construction-site inspection to do exactly that (see
	// candidatesFromAudit's doc comment, findings.go). This is what closes
	// TBD-10: a stdlib client like net/http never appears in a lockfile
	// (R-DET-5), so detect.Dep.Clients alone can never carry it into
	// Candidates no matter how complete this package's own knob table is —
	// doctor.Audit is the one place already permitted to see it. Same dir
	// argument cmd/tortureu's own doctor command uses (filepath.Dir of the
	// compose file) — this package still never opens a source file itself.
	auditFindings := doctor.Audit(filepath.Dir(cfg.Target.Compose), &sys)

	// R-EXE-26/R-EXE-27: stop the co-driven tools and fold what they
	// produced into this run's one verdict — that is what `drive` tier
	// means. A tool that could not run at all is TortureU failing
	// (status: error); the failures a fuzzer found are the SUT failing and
	// become findings below, never an error (R-VER-2).
	var fuzzFindings []verdict.Finding
	if drivers != nil {
		var driveErr error
		fuzzFindings, driveErr = applyDriveResults(v, drivers.collect(), cfg, sys, auditFindings, drivers.everStarted())
		if driveErr != nil {
			teardownAll()
			return fail("co-driven load source failed", driveErr)
		}
	}

	thresholdPassed, thresholdFindings := evaluateThresholds(metrics, cfg.Faults, sys, auditFindings)
	promPassed, promFindings := evaluatePromqlAsserts(cfg.Assert, deps.Prom, cfg.Faults, sys, auditFindings)
	sqlFindings := evaluateSQLAsserts(cfg.Assert)

	// 6. Tear down every fault (R-EXE-5), before the verdict is emitted.
	teardownAll()

	v.Passed = append(thresholdPassed, promPassed...)
	// IDs are assigned once, here, after every finding source is merged —
	// not inside evaluateThresholds/evaluatePromqlAsserts/evaluateSQLAsserts,
	// each of which used to number its own findings from f1: two sources
	// could then emit the same ID, making one of them unaddressable by an
	// agent calling explain_failure (it returns the first match).
	findings := append(append(append(thresholdFindings, promFindings...), sqlFindings...), fuzzFindings...)
	for i := range findings {
		findings[i].ID = fmt.Sprintf("f%d", i+1)
	}
	v.Findings = findings

	if warning, ok := throughputWarning(cfg, metrics); ok {
		addWarning(v, warning)
	}

	status := verdict.StatusPass
	if len(v.Findings) > 0 {
		status = verdict.StatusFail
	}
	return finish(status)
}

// addWarning appends msg to the verdict's Artifacts["warnings"] list.
// verdict.Verdict has no dedicated warnings field (escalated in the Task 7
// report); Artifacts is the least-invented place to put one without editing
// the read-only verdict package. Used for R-EXE-4's throughput-shortfall
// warning and R-EXE-22's error_rate rate-approximation note — both
// informational, neither a reason to fail the run.
func addWarning(v *verdict.Verdict, msg string) {
	if v.Artifacts == nil {
		v.Artifacts = map[string]any{}
	}
	v.Artifacts["warnings"] = append(asStringSlice(v.Artifacts["warnings"]), msg)
}

// discoverSUTContainer finds the actual Docker container name compose
// created for service, by the same compose-applied label ComposeTopologyApplier's
// own Apply brings up, mirroring the discovery pattern this package's
// Docker-backed tests already use (findContainer in dc2_enforcement_test.go)
// — a package var, not a plain function, so a test can substitute a fake
// without needing a live daemon to exercise Run's wiring around it (see
// TestRun_WiresSUTContainerIntoLoadRunnerAfterTopologyApply).
var discoverSUTContainer = func(service string) (string, error) {
	out, err := exec.Command("docker", "ps", "--filter", "label=com.docker.compose.service="+service, "--format", "{{.Names}}").Output()
	if err != nil {
		return "", fmt.Errorf("docker ps: %w", err)
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return "", fmt.Errorf("no running container found for compose service %q", service)
	}
	return names[0], nil
}

// internalNetworkFaultTargets returns the deduplicated "host:port" targets
// of every fault whose target is classified internal and whose verb needs
// network-level interception (R-EXE-20) — never faults targeting a
// container-scoped verb (cpu, pause, ...), which reach their target via
// DockerApplier directly and need no proxy at all.
func internalNetworkFaultTargets(faults []config.Fault, classes map[string]egress.Class) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range faults {
		if classes[f.Target] != egress.ClassInternal || !networkFaultVerbs[f.Verb] {
			continue
		}
		if !seen[f.Target] {
			seen[f.Target] = true
			out = append(out, f.Target)
		}
	}
	return out
}

func asStringSlice(v any) []string {
	s, _ := v.([]string)
	return s
}

// throughputWarning implements R-EXE-4: if achieved throughput trails the
// declared target by more than 5%, the verdict must carry a warning. There
// is no `warnings` field in verdict.Verdict (escalated in the Task 7
// report); this package surfaces it via Artifacts["warnings"] as the least
// invented place to put it without editing the read-only verdict package.
func throughputWarning(cfg *config.Config, metrics map[string]any) (string, bool) {
	target := maxTargetRPS(cfg.Load.Stages)
	if target <= 0 {
		return "", false
	}
	achieved, ok := httpReqsRate(metrics)
	if !ok {
		return "", false
	}
	if achieved >= target*0.95 {
		return "", false
	}
	return fmt.Sprintf("achieved throughput %.1frps trails target %.1frps by more than 5%% — the load generator may be the bottleneck", achieved, target), true
}

func maxTargetRPS(stages []config.Stage) float64 {
	var max float64
	for _, s := range stages {
		v := s.To
		if v == "" {
			v = s.Hold
		}
		v = strings.TrimSuffix(v, "rps")
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil && f > max {
			max = f
		}
	}
	return max
}

func httpReqsRate(metrics map[string]any) (float64, bool) {
	m, ok := metrics["http_reqs"].(map[string]any)
	if !ok {
		return 0, false
	}
	values, ok := m["values"].(map[string]any)
	if !ok {
		values = m
	}
	rate, ok := values["rate"].(float64)
	return rate, ok
}
