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
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	realapplier "github.com/jdb316/tortureu/internal/applier"
	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
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
	Prom        PromQuerier
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
	finish := func(status verdict.Status) *verdict.Verdict {
		v.Status = status
		v.DurationS = int(now().Sub(started).Seconds())
		return v
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
	teardownAll := func() {
		manager.Teardown() // R-EXE-5
		teMu.Lock()
		f := teardownExpiring
		teMu.Unlock()
		f()
	}

	// R-EXE-16: a signal must tear everything down no matter when it
	// arrives — before load starts, mid-load, or in the window between the
	// load finishing and the scheduler's own goroutines draining. A
	// previous version only checked for a caught signal inside the one
	// select guarding handle.Done(), so a signal in any other window ran
	// fault.Manager's own teardown (which manager.WatchSignals triggers
	// internally, unconditionally) but never reached teardownExpiring,
	// silently leaving for:-duration faults applied. This watcher runs for
	// Run's entire body instead, so there is no such window.
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
		return finish(verdict.StatusError)
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
			return finish(verdict.StatusError)
		}
	}

	script, err := k6.Compile(cfg)
	if err != nil {
		return finish(verdict.StatusError)
	}
	if av, ok := abortedEarly(); ok {
		return av
	}

	// 4. Start load; schedule faults against k6's phase markers (R-EXE-8).
	handle, err := deps.Load.Start(script)
	if err != nil {
		return finish(verdict.StatusError)
	}
	schedDone, expiringTeardown := scheduleFaults(cfg.Faults, handle.Markers(), started, manager, deps.Applier, deps.QueueApplier, deps.MockApplier, classes)
	setTeardownExpiring(expiringTeardown)

	var result LoadResult
	select {
	case result = <-handle.Done():
	case <-caught:
		// teardownAll already ran inside the signal watcher goroutine.
		return finish(verdict.StatusAborted)
	case loadErr := <-handle.Err():
		_ = loadErr
		teardownAll()
		return finish(verdict.StatusError)
	}
	if av, ok := abortedEarly(); ok {
		return av
	}
	// R-EXE-19: a fault that couldn't be routed to a wired owner fails the
	// run instead of silently proceeding as if it had been applied.
	if schedErrs := <-schedDone; len(schedErrs) > 0 {
		teardownAll()
		return finish(verdict.StatusError)
	}
	if av, ok := abortedEarly(); ok {
		return av
	}

	metrics, err := k6.IngestSummary(result.SummaryJSON)
	if err != nil {
		teardownAll()
		return finish(verdict.StatusError)
	}
	v.Metrics = metrics

	thresholdPassed, thresholdFindings := evaluateThresholds(metrics, cfg.Faults, sys)
	promPassed, promFindings := evaluatePromqlAsserts(cfg.Assert, deps.Prom, cfg.Faults, sys)

	// 6. Tear down every fault (R-EXE-5), before the verdict is emitted.
	teardownAll()

	v.Passed = append(thresholdPassed, promPassed...)
	v.Findings = append(thresholdFindings, promFindings...)

	if warning, ok := throughputWarning(cfg, metrics); ok {
		if v.Artifacts == nil {
			v.Artifacts = map[string]any{}
		}
		v.Artifacts["warnings"] = append(asStringSlice(v.Artifacts["warnings"]), warning)
	}

	status := verdict.StatusPass
	if len(v.Findings) > 0 {
		status = verdict.StatusFail
	}
	return finish(status)
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
