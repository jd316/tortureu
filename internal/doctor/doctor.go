// Package doctor is a static resilience audit and registry coverage report.
//
// It never executes anything and never fails a build: its findings are
// hints derived from internal/detect's compose+manifest view of a system
// (R-DET-1), pointing at what a run could prove — not proof itself
// (R-AUD-3).
//
// Precision limit (source inspection, R-AUD-5/6): once a construction site
// is attributed to a dependency, inspection scans that whole file for
// timeout/retry signals, not just the constructor call's own arguments. A
// signal placed elsewhere in the same file (e.g. an unrelated
// context.WithTimeout) can therefore count as evidence. This is a
// deliberate, bounded (single known file, known library) heuristic, not a
// per-argument guarantee — treat a "confirmed" finding as strong evidence,
// still worth the experiment named in R-AUD-4, not as certainty.
package doctor

import (
	"fmt"

	"github.com/jdb316/tortureu/internal/detect"
)

// Level marks a Finding as informational only. There is exactly one value:
// findings are never failures (R-AUD-3), so there is nothing to escalate to.
type Level string

// LevelHint is the only Level a Finding can carry (R-AUD-3).
const LevelHint Level = "hint"

// Check names which resilience knob a Finding is about.
type Check string

const (
	// CheckTimeout is R-AUD-1: retries and circuit breakers are inert
	// behind an infinite timeout, so its absence is the highest-yield
	// finding.
	CheckTimeout Check = "timeout"
	// CheckRetry is R-AUD-2: retry configuration lacking a cap, backoff,
	// or jitter is an overload source, not a mitigation.
	CheckRetry Check = "retry"
)

// Finding is one static resilience-audit result (R-AUD-1/2). It is always a
// hint (R-AUD-3, Level) and names the experiment that would turn the static
// hint into a run-proven result (R-AUD-4).
//
// Determined and Present together carry R-AUD-6's three-way outcome:
// Determined=false means the audit could not tell (Present is meaningless
// and always false); Determined=true, Present=false means the knob was
// confirmed absent by bounded source inspection; Determined=true,
// Present=true means it was confirmed present. "Not determined" and "not
// configured" must never be conflated.
type Finding struct {
	DepName    string // detect.Dep.Name this finding is about
	DepType    string // detect.Dep.Type (R-DET-9 vocabulary)
	Library    string // the client library import that triggered this finding (R-AUD-5)
	Check      Check
	Level      Level
	Determined bool
	Present    bool
	Hint       string
	Experiment string
}

// faultForCheck names the fault verb (SPEC.md §4.4 / internal/fault) that
// would exercise the gap a Check flags (R-AUD-4). A slow dependency proves
// whether a timeout exists; an outage proves whether retries have a cap.
var faultForCheck = map[Check]string{
	CheckTimeout: "latency",
	CheckRetry:   "down",
}

// Audit runs the static resilience checks (R-AUD-1, R-AUD-2) over sys,
// rooted at dir.
//
// For a dependency with a lockfile-sourced client (R-DET-5), it inspects
// the bounded construction site doctor's own table knows for that client's
// type (R-AUD-5) — never at dependencies known only from a compose image,
// never at arbitrary control flow, and never at a library outside the
// table. For a dependency with no lockfile-sourced client, it still checks
// for Go's net/http (TBD-10): net/http is stdlib, so it never appears in a
// go.mod require line and R-DET-5 can never see it, but R-AUD-5 permits the
// audit itself to read source at a known construction site — finding
// http.Client{ there is the evidence, independent of any manifest. A
// dependency with neither yields no finding; that is honest, not a gap
// (R-AUD-6 only requires "not determined" be said when a known library was
// checked and couldn't be resolved, not that every dependency get a
// finding).
//
// Findings are always hints (R-AUD-3): this is a static check; only a run
// proves anything. Where inspection cannot determine a setting, the
// finding says so explicitly rather than asserting absence (R-AUD-6).
func Audit(dir string, sys *detect.System) []Finding {
	var findings []Finding
	if sys == nil {
		return findings
	}
	for _, dep := range sys.Deps {
		if len(dep.Clients) > 0 {
			library := dep.Clients[0]
			findings = append(findings, buildFinding(dep, dep.Type, library, CheckTimeout, inspectTimeout(dir, dep.Type)))
			findings = append(findings, buildFinding(dep, dep.Type, library, CheckRetry, inspectRetry(dir, dep.Type)))
			continue
		}
		if !siteHasEvidence(dir, "http") {
			continue // no evidence net/http is used anywhere: silence, not a guess either way
		}
		findings = append(findings, buildFinding(dep, "http", "net/http", CheckTimeout, inspectTimeout(dir, "http")))
		findings = append(findings, buildFinding(dep, "http", "net/http", CheckRetry, inspectRetry(dir, "http")))
	}
	return findings
}

// buildFinding renders an inspectResult into a Finding, wording the hint
// according to R-AUD-6's three-way outcome. siteType names the
// goSourceSites entry inspection actually used — dep.Type for a
// lockfile-sourced client, or the fixed "http" for net/http — since the two
// can differ (a dependency's own detected type says nothing about which
// stdlib library the SUT happens to use to reach it).
func buildFinding(dep detect.Dep, siteType, library string, check Check, res inspectResult) Finding {
	f := Finding{
		DepName:    dep.Name,
		DepType:    siteType,
		Library:    library,
		Check:      check,
		Level:      LevelHint,
		Determined: res.determined,
		Present:    res.determined && res.present,
		Experiment: experimentFor(check, dep),
	}

	noun := "a timeout"
	if check == CheckRetry {
		noun = "a capped, backed-off, jittered retry"
	}

	switch {
	case !res.determined:
		f.Hint = fmt.Sprintf(
			"not determined whether %s is configured for %s client %s: %s",
			noun, siteType, library, res.reason)
	case res.present:
		f.Hint = fmt.Sprintf("%s confirmed configured for %s client %s", noun, siteType, library)
	default:
		f.Hint = fmt.Sprintf("%s not configured for %s client %s", noun, siteType, library)
	}
	return f
}

// experimentFor names the fault (R-AUD-4) that would prove or disprove the
// finding for check against dep: e.g. a missing-timeout finding on the
// Postgres client names the exact `latency` fault against postgres that
// would demonstrate the consequence.
func experimentFor(check Check, dep detect.Dep) string {
	verb := faultForCheck[check]
	switch check {
	case CheckTimeout:
		return fmt.Sprintf("fault: %s on %s — a slow dependency proves whether a request deadline exists", verb, dep.Name)
	case CheckRetry:
		return fmt.Sprintf("fault: %s on %s — an outage proves whether retries are capped, back off, and jitter", verb, dep.Name)
	default:
		return fmt.Sprintf("fault: %s on %s", verb, dep.Name)
	}
}
