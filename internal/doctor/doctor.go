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
type Finding struct {
	DepName    string // detect.Dep.Name this finding is about
	DepType    string // detect.Dep.Type (R-DET-9 vocabulary)
	Library    string // the client library import that triggered this finding (R-AUD-5)
	Check      Check
	Level      Level
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

// Audit runs the static resilience checks (R-AUD-1, R-AUD-2) over sys.
//
// It only looks at dependencies where detect already identified a client
// library from a manifest (R-DET-5) — a "known library's known
// construction site" per R-AUD-5 — never at dependencies known only from a
// compose image, and never at application source. Findings are always
// hints (R-AUD-3): this is a static check; only a run proves anything.
func Audit(sys *detect.System) []Finding {
	var findings []Finding
	if sys == nil {
		return findings
	}
	for _, dep := range sys.Deps {
		if len(dep.Clients) == 0 {
			continue // no known client library at this dependency: out of R-AUD-5's scope
		}
		library := dep.Clients[0]

		findings = append(findings, Finding{
			DepName: dep.Name,
			DepType: dep.Type,
			Library: library,
			Check:   CheckTimeout,
			Level:   LevelHint,
			Hint: fmt.Sprintf(
				"no timeout configuration could be confirmed for %s client %s from static detection — "+
					"an unset timeout leaves retries and circuit breakers inert",
				dep.Type, library),
			Experiment: experimentFor(CheckTimeout, dep),
		})

		findings = append(findings, Finding{
			DepName: dep.Name,
			DepType: dep.Type,
			Library: library,
			Check:   CheckRetry,
			Level:   LevelHint,
			Hint: fmt.Sprintf(
				"retry configuration for %s client %s could not be confirmed to have a cap, backoff, and jitter from static detection — "+
					"an uncapped retry is an overload source, not a mitigation",
				dep.Type, library),
			Experiment: experimentFor(CheckRetry, dep),
		})
	}
	return findings
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
