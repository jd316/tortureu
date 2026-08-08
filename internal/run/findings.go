package run

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/verdict"
)

// confidenceFor implements as much of the R-VER-3 table as this package can
// honestly claim: `caused` requires traces spanning the fault window, and no
// trace-ingestion pipeline exists anywhere in the built packages yet (no
// package here parses spans or joins them to a fault window), so this
// package never emits `caused` — doing so would be inventing a capability
// SPEC.md requires evidence for for and reporting it as fact (R-PROC-2/4).
// That gap is escalated in the Task 7 report rather than papered over here.
//
// `correlated` requires exactly one fault active in the breach window; this
// package does not have per-metric breach timestamps from k6's aggregate
// end-of-run summary (R-VER-10 forbids parsing anything richer than that
// JSON), so "exactly one fault active in the breach window" is approximated
// as "exactly one fault was scheduled for the whole run" — the narrowest
// honest reading available from the data this package actually has.
// Anything else (zero faults, or more than one) is `ambiguous`, per the
// table's own "ambiguous requires >=2 candidate causes" — zero faults isn't
// literally >=2, but SPEC.md does not define a confidence for a breach with
// no candidate cause at all, so this defaults to the least confident label
// rather than fabricating a middle one. Also escalated.
func confidenceFor(activeFaults int) verdict.Confidence {
	if activeFaults == 1 {
		return verdict.Correlated
	}
	return verdict.Ambiguous
}

// clientKnobPatterns maps a substring of a detected client import path
// (detect.Dep.Clients, e.g. "github.com/jackc/pgx/v5") to the config knobs
// a candidate config surface (R-VER-4, D-9) should name for it. This is a
// small, bounded, hand-curated table — the same posture R-AUD-5 takes for
// known-library audits — not general source inspection: a client whose
// import path matches nothing here gets no knobs, never a guessed one (see
// buildCandidate's doc comment).
var clientKnobPatterns = []struct {
	substr string
	knobs  []string
}{
	{"jackc/pgx", []string{"MaxConns", "MinConns", "ConnConfig.ConnectTimeout"}},
	{"lib/pq", []string{"MaxOpenConns", "MaxIdleConns", "ConnMaxLifetime"}},
	{"go-redis/redis", []string{"PoolSize", "DialTimeout", "ReadTimeout", "WriteTimeout"}},
	{"redis/go-redis", []string{"PoolSize", "DialTimeout", "ReadTimeout", "WriteTimeout"}},
	{"gomodule/redigo", []string{"MaxIdle", "MaxActive", "IdleTimeout"}},
	{"cenkalti/backoff", []string{"MaxRetries", "InitialInterval", "MaxElapsedTime"}},
}

// manifestFor names the manifest file a detected client library was almost
// certainly declared in, from detect.System.Lang — the closest thing this
// package has to D-9's `source` field, since detect.Dep.Clients records
// only the import path, not which manifest it came from.
func manifestFor(lang string) string {
	switch lang {
	case "go":
		return "go.mod"
	case "node":
		return "package.json"
	case "python":
		return "pyproject.toml"
	default:
		return ""
	}
}

// depForTarget finds the detected dependency whose address matches a
// fault's target ("host:port", the same shape config.Fault.Target and
// detect.Dep.Address both use), or nil if none was detected — an external
// or otherwise-undetected target simply has no candidate config surface to
// report, which is the honest answer, not an error.
func depForTarget(deps []detect.Dep, target string) *detect.Dep {
	for i := range deps {
		if deps[i].Address == target {
			return &deps[i]
		}
	}
	return nil
}

// knobsFor returns the known config knobs for a client import path, or nil
// if this package has no curated entry for it — never a guess (the honesty
// rule this codebase applies everywhere else: R-AUD-6, R-DC2-6, and this
// package's own refusal to ever emit `caused` without a trace pipeline).
func knobsFor(client string) []string {
	for _, p := range clientKnobPatterns {
		if strings.Contains(client, p.substr) {
			return p.knobs
		}
	}
	return nil
}

// buildCandidates turns one fault's detected dependency into the D-9
// candidate list (R-VER-4): library + known knobs, one entry per client
// library the target dependency was detected using. A target with no
// matching detect.Dep (an external host, or one detection simply never
// saw) produces no candidates — not a fabricated one.
func buildCandidates(f config.Fault, deps []detect.Dep, lang string) []verdict.Candidate {
	dep := depForTarget(deps, f.Target)
	if dep == nil {
		return nil
	}
	source := manifestFor(lang)
	candidates := make([]verdict.Candidate, 0, len(dep.Clients))
	for _, client := range dep.Clients {
		candidates = append(candidates, verdict.Candidate{
			Library: client,
			Source:  source,
			Knobs:   knobsFor(client),
		})
	}
	return candidates
}

// unevaluatedFinding represents an assertion this package could not
// evaluate at all — distinct from both a held assertion (Passed) and a
// genuinely broken one (R-VER-8: "a green that means we couldn't tell" is
// exactly the failure mode this exists to prevent; R-COV-6: "unevaluable
// must never read as false" — nor, by the same reasoning, as true).
// Confidence is Ambiguous so verdict.ExitCode's existing "status: fail with
// every finding ambiguous => exit 4 (inconclusive)" rule fires when every
// finding in a run is one of these — verdict.Verdict has no separate
// "unevaluated" list of its own to populate instead (escalated in the
// Task 7 report: a dedicated field would be cleaner than overloading
// Findings this way, but that is internal/verdict's call, not this
// package's to make unilaterally).
func unevaluatedFinding(assertion, reason string) verdict.Finding {
	return verdict.Finding{
		Confidence: verdict.Ambiguous,
		Broke:      verdict.Broke{Assertion: assertion, Observed: "not evaluated: " + reason},
	}
}

// evaluateSQLAsserts reports every sql: assert entry (R-CFG-18) as
// unevaluated: no package anywhere in this codebase evaluates a SQL
// assertion against a real database, so the honest answer for every one,
// always, is "not evaluated" — never a silent skip (R-VER-8) and never a
// guessed pass or fail.
func evaluateSQLAsserts(asserts []config.AssertEntry) []verdict.Finding {
	var findings []verdict.Finding
	for _, entry := range asserts {
		expr, ok := entry["sql"].(string)
		if !ok {
			continue
		}
		findings = append(findings, unevaluatedFinding("sql: "+expr, "no SQL evaluation capability exists in this build"))
	}
	return findings
}

// thresholdComparisonOps are tried in this order only to find the earliest
// occurring operator in an expression; "<=" appearing before "<" would
// otherwise make no difference since both start at the same index for an
// expression that actually contains "<=".
var thresholdComparisonOps = []string{"<=", ">=", "==", "!=", "<", ">"}

// thresholdStatKey extracts the k6 summary statistic name a threshold
// expression names — everything before its comparison operator
// ("p(95)<500" -> "p(95)", "rate<0.01" -> "rate"). ok is false when no
// known comparison operator appears at all.
func thresholdStatKey(expr string) (string, bool) {
	idx := -1
	for _, op := range thresholdComparisonOps {
		if i := strings.Index(expr, op); i != -1 && (idx == -1 || i < idx) {
			idx = i
		}
	}
	if idx == -1 {
		return "", false
	}
	return strings.TrimSpace(expr[:idx]), true
}

// measuredValue reads the actual measured statistic a threshold expression
// names out of k6's own per-metric "values" object (VERDICT.md §1's
// "observed": "4218ms" — a real measured value, not a restatement of
// pass/fail: the ✗/✓ already says that). Metrics k6 marks
// `"contains": "time"` get k6's own "ms" unit appended; anything else
// (rate, count) is unitless, matching k6's own summary. ok is false
// whenever the value genuinely cannot be read — no "values" object, an
// unrecognized stat key, or a non-numeric value — so the caller reports
// "not measured" instead of fabricating a number (the honesty rule this
// package applies everywhere: never emit `caused` without traces, never a
// guessed knob, and now never a value that wasn't actually read).
func measuredValue(m map[string]any, expr string) (string, bool) {
	statKey, ok := thresholdStatKey(expr)
	if !ok {
		return "", false
	}
	values, ok := m["values"].(map[string]any)
	if !ok {
		return "", false
	}
	raw, ok := values[statKey]
	if !ok {
		return "", false
	}
	v, ok := raw.(float64)
	if !ok {
		return "", false
	}
	formatted := strconv.FormatFloat(v, 'f', -1, 64)
	if contains, _ := m["contains"].(string); contains == "time" {
		formatted += "ms"
	}
	return formatted, true
}

// faultWindow renders a fault's declared anchor as the two-element
// [start, end] window VERDICT.md's Cause.window carries. end is only
// meaningful when for: is present (R-CFG-10: absent means "until end of
// run", which has no fixed end to name).
func faultWindow(f config.Fault) []string {
	if f.For == "" {
		return []string{f.At}
	}
	return []string{f.At, f.At + "+" + f.For}
}

// attribute fills in a finding's Cause and Candidates from the faults
// active during this run (R-VER-3, R-VER-4, D-9). Cause is only set when
// there is exactly one candidate fault — the same condition confidenceFor
// calls `correlated` — since attributing to a specific fault when >=2 were
// active would be exactly the fabrication `ambiguous` exists to prevent.
// Candidates, by contrast, are legitimately a list: D-4 defines `ambiguous`
// as ">=2 candidate causes", so every active fault's target contributes its
// own candidate config surface regardless of how many there are.
func attribute(f *verdict.Finding, faults []config.Fault, deps []detect.Dep, lang string) {
	if len(faults) == 1 {
		c := faults[0]
		f.Cause = &verdict.Cause{
			Fault:  c.Name,
			Target: c.Target,
			Inject: c.Inject,
			Window: faultWindow(c),
		}
	}
	for _, fault := range faults {
		f.Candidates = append(f.Candidates, buildCandidates(fault, deps, lang)...)
	}
}

// evaluateThresholds reads k6's per-metric threshold results out of
// IngestSummary's metrics map (k6's own JSON shape: metrics[name].thresholds
// is {expr: {ok: bool}}) and turns each into a Passed or Finding entry
// (R-VER-3, R-VER-5). Metrics with no thresholds sub-object are not
// k6-threshold assertions (they're plain metrics, or promql/sql entries
// internal/k6 already passes over) and are skipped here. faults are every
// fault declared for this run (used for Cause/Candidates attribution, see
// attribute); deps are internal/detect's dependency list (D-9's client
// libraries, for Candidates). Both Passed and Finding entries carry the
// actual measured statistic (see measuredValue), falling back to the
// honest "not measured" only when it genuinely cannot be read.
func evaluateThresholds(metrics map[string]any, faults []config.Fault, sys detect.System) ([]verdict.Passed, []verdict.Finding) {
	var passed []verdict.Passed
	var findings []verdict.Finding

	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	// Deterministic order for stable verdicts across runs with identical data.
	sortStrings(names)

	for _, name := range names {
		m, ok := metrics[name].(map[string]any)
		if !ok {
			continue
		}
		thresholds, ok := m["thresholds"].(map[string]any)
		if !ok {
			continue
		}
		exprs := make([]string, 0, len(thresholds))
		for expr := range thresholds {
			exprs = append(exprs, expr)
		}
		sortStrings(exprs)

		for _, expr := range exprs {
			result, _ := thresholds[expr].(map[string]any)
			ok, _ := result["ok"].(bool)
			assertion := fmt.Sprintf("%s: %s", name, expr)
			observed, measured := measuredValue(m, expr)
			if !measured {
				observed = "not measured"
			}
			if ok {
				passed = append(passed, verdict.Passed{Assertion: assertion, Observed: observed})
				continue
			}
			finding := verdict.Finding{
				Confidence: confidenceFor(len(faults)),
				Broke: verdict.Broke{
					Assertion: assertion,
					Observed:  observed,
				},
			}
			attribute(&finding, faults, sys.Deps, sys.Lang)
			findings = append(findings, finding)
		}
	}
	return passed, findings
}

// evaluatePromqlAsserts evaluates every promql: entry in assert: (R-CFG-17)
// — the signals k6 cannot observe. A nil querier means no Prometheus
// endpoint was configured (-prom-url was empty); such entries are reported
// as unevaluated (R-VER-8, R-COV-6), never silently dropped and never
// treated as passing (an unrun assertion must not look like a held one,
// R-VER-5). faults/deps feed Cause/Candidates attribution, same as
// evaluateThresholds. IDs are assigned once by the caller after every
// finding source is merged (Run, run.go) — not here, where two independent
// slices numbering from f1 would collide once combined.
func evaluatePromqlAsserts(asserts []config.AssertEntry, querier PromQuerier, faults []config.Fault, sys detect.System) ([]verdict.Passed, []verdict.Finding) {
	var passed []verdict.Passed
	var findings []verdict.Finding
	for _, entry := range asserts {
		expr, ok := entry["promql"].(string)
		if !ok {
			continue
		}
		assertion := "promql: " + expr
		if querier == nil {
			findings = append(findings, unevaluatedFinding(assertion, "no Prometheus endpoint configured (-prom-url)"))
			continue
		}
		holds, observed, err := querier.Query(expr)
		if err != nil {
			findings = append(findings, verdict.Finding{
				Confidence: verdict.Ambiguous,
				Broke:      verdict.Broke{Assertion: assertion, Observed: "query error: " + err.Error()},
			})
			continue
		}
		if holds {
			passed = append(passed, verdict.Passed{Assertion: assertion, Observed: observed})
			continue
		}
		finding := verdict.Finding{
			Confidence: confidenceFor(len(faults)),
			Broke:      verdict.Broke{Assertion: assertion, Observed: observed},
		}
		attribute(&finding, faults, sys.Deps, sys.Lang)
		findings = append(findings, finding)
	}
	return passed, findings
}

// sortStrings avoids importing sort at every call site for what is always a
// small slice.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
