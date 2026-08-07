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
	"strings"
	"time"

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
// first request is generated (R-EXE-3).
type TopologyApplier interface {
	Apply(composePath string, top egress.Topology) error
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

// Deps is every external seam Run needs. Production callers get one from
// NewRealDeps (docker_applier.go, toxiproxy_applier.go, promql.go, load.go,
// topology.go, reset.go); tests substitute fakes for some or all fields.
type Deps struct {
	Reset    Resetter
	Topology TopologyApplier
	Load     LoadRunner
	Applier  fault.Applier
	// QueueApplier is internal/queuefault's real broker client, the owner
	// R-EXE-15 names for poison_pill/duplicate. nil means none is wired: a
	// run declaring either verb then fails per R-EXE-19 rather than
	// silently skipping the fault.
	QueueApplier queuefault.Applier
	Prom         PromQuerier
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
func Run(cfg *config.Config, sys detect.System, deps Deps, opts Options) *verdict.Verdict {
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
	caught, stopSignals := manager.WatchSignals() // R-EXE-16
	defer stopSignals()

	// teardownExpiring is reassigned once scheduleFaults starts (it tracks
	// the for:-duration faults applied outside fault.Manager, see
	// scheduler.go); the panic-recover below closes over the variable, not
	// its zero value, so it still reaches those faults if the panic happens
	// after scheduling begins.
	teardownExpiring := func() {}

	defer func() {
		if r := recover(); r != nil {
			manager.Teardown() // R-EXE-5: teardown on panic
			teardownExpiring()
			v.Status = verdict.StatusError
			v.DurationS = int(now().Sub(started).Seconds())
		}
	}()

	// 1. Reset (R-CFG-20/21, R-EXE-2), unless --no-reset.
	if opts.NoReset {
		v.Reset = "skipped"
	} else if err := deps.Reset.Reset(cfg.Reset.Command); err != nil {
		v.Reset = "failed"
		return finish(verdict.StatusAborted)
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

	// 3. Apply the topology overlay so egress is enforced (R-DC2-3, R-EXE-3)
	// before load starts generating requests.
	top := egress.BuildTopology(sutNetworkName, egressNetworkName, proxyServiceName)
	if err := deps.Topology.Apply(cfg.Target.Compose, top); err != nil {
		return finish(verdict.StatusError)
	}

	script, err := k6.Compile(cfg)
	if err != nil {
		return finish(verdict.StatusError)
	}

	// 4. Start load; schedule faults against k6's phase markers (R-EXE-8).
	handle, err := deps.Load.Start(script)
	if err != nil {
		return finish(verdict.StatusError)
	}
	var schedDone <-chan []error
	schedDone, teardownExpiring = scheduleFaults(cfg.Faults, handle.Markers(), started, manager, deps.Applier, deps.QueueApplier)
	teardownAll := func() {
		manager.Teardown()
		teardownExpiring()
	}

	var result LoadResult
	select {
	case result = <-handle.Done():
	case <-caught:
		teardownAll()
		return finish(verdict.StatusAborted)
	case loadErr := <-handle.Err():
		_ = loadErr
		teardownAll()
		return finish(verdict.StatusError)
	}
	// R-EXE-19: a fault that couldn't be routed to a wired owner fails the
	// run instead of silently proceeding as if it had been applied.
	if schedErrs := <-schedDone; len(schedErrs) > 0 {
		teardownAll()
		return finish(verdict.StatusError)
	}

	metrics, err := k6.IngestSummary(result.SummaryJSON)
	if err != nil {
		teardownAll()
		return finish(verdict.StatusError)
	}
	v.Metrics = metrics

	activeFaults := len(cfg.Faults)
	thresholdPassed, thresholdFindings := evaluateThresholds(metrics, activeFaults)
	promPassed, promFindings := evaluatePromqlAsserts(cfg.Assert, deps.Prom, activeFaults)

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
