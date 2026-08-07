// Package doctor is a static resilience audit and registry coverage report.
//
// It never executes anything and never fails a build: its findings are
// hints derived from internal/detect's compose+manifest view of a system
// (R-DET-1), pointing at what a run could prove — not proof itself
// (R-AUD-3).
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
// It only looks at dependencies where detect already identified a client
// library from a manifest (R-DET-5), and only at the bounded construction
// site doctor's own table knows for that library's type (R-AUD-5) — never
// at dependencies known only from a compose image, never at arbitrary
// control flow, and never at a library outside the table. Findings are
// always hints (R-AUD-3): this is a static check; only a run proves
// anything. Where inspection cannot determine a setting, the finding says
// so explicitly rather than asserting absence (R-AUD-6).
func Audit(dir string, sys *detect.System) []Finding {
	var findings []Finding
	if sys == nil {
		return findings
	}
	for _, dep := range sys.Deps {
		if len(dep.Clients) == 0 {
			continue // no known client library at this dependency: out of R-AUD-5's scope
		}
		library := dep.Clients[0]

		findings = append(findings, buildFinding(dep, library, CheckTimeout, inspectTimeout(dir, dep.Type)))
		findings = append(findings, buildFinding(dep, library, CheckRetry, inspectRetry(dir, dep.Type)))
	}
	return findings
}

// buildFinding renders an inspectResult into a Finding, wording the hint
// according to R-AUD-6's three-way outcome.
func buildFinding(dep detect.Dep, library string, check Check, res inspectResult) Finding {
	f := Finding{
		DepName:    dep.Name,
		DepType:    dep.Type,
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
			noun, dep.Type, library, res.reason)
	case res.present:
		f.Hint = fmt.Sprintf("%s confirmed configured for %s client %s", noun, dep.Type, library)
	default:
		f.Hint = fmt.Sprintf("%s not configured for %s client %s", noun, dep.Type, library)
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
