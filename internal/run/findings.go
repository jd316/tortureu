package run

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/doctor"
	"github.com/jdb316/tortureu/internal/verdict"
)

// confidenceFor implements the fault-count half of the R-VER-3 table: what
// can be said from TortureU's own side of the wire, with no cooperation
// from the target. `caused` is deliberately not reachable from here — it
// requires traces spanning the fault window, which is evidence only the
// target's telemetry can supply — so it is applied afterwards, by
// applyTraceChain (trace.go), and only when a chain was actually built from
// real ingested spans (R-VER-13, R-VER-14). A finding whose traces could
// not be read keeps whatever this function returned, which is exactly what
// every finding carried before ingestion existed.
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
	// net/http: Go's standard HTTP client, by a wide margin the most common
	// one — and, per an E1 finding, the client on three of its fault-driven
	// cases (including case 1, "HTTP client with no timeout," the canonical
	// resilience defect), none of which carried candidates because this
	// table had no entry for it at all. Every name below is a real,
	// existing exported field (never a guess, matching this table's own
	// rule): Client.Timeout is the whole-request deadline case 1 plants the
	// absence of; Transport's ResponseHeaderTimeout/DialContext/
	// TLSHandshakeTimeout are the finer-grained per-phase deadlines;
	// Transport.MaxIdleConnsPerHost is the connection-pool knob — net/http's
	// equivalent of the pgx pool-exhaustion knob above.
	{"net/http", []string{"Client.Timeout", "Transport.ResponseHeaderTimeout", "Transport.DialContext", "Transport.TLSHandshakeTimeout", "Transport.MaxIdleConnsPerHost"}},
	// Java clients (R-DET-14's pom.xml support). Detection records Maven
	// coordinates, which are distinctive enough for substring matching.
	// Every name below is a real property of the named library, checked
	// against its own documentation — the same rule the rest of this table
	// follows, and the reason `pool` knobs are attached to the pool and
	// driver knobs to the driver rather than mixed.
	{"org.postgresql:postgresql", []string{"connectTimeout", "socketTimeout", "loginTimeout"}},
	// HikariCP is the connection pool behind Spring Boot's JPA starter.
	// It is listed as its own entry rather than folded into the JDBC
	// driver's knobs because they are different objects with different
	// settings — and it fires only when the pool itself is a detected
	// client, which internal/detect does not currently record (R-AUD-5: a
	// knob attributed to a library we did not see would be a guess).
	{"com.zaxxer:HikariCP", []string{"maximumPoolSize", "connectionTimeout", "maxLifetime"}},
	{"redis.clients:jedis", []string{"JedisPoolConfig.maxTotal", "JedisClientConfig.connectionTimeoutMillis", "JedisClientConfig.socketTimeoutMillis"}},
	{"io.lettuce:lettuce-core", []string{"RedisURI.timeout", "SocketOptions.connectTimeout"}},
	{"kafka-clients", []string{"request.timeout.ms", "delivery.timeout.ms", "max.block.ms"}},
	{"spring-kafka", []string{"request.timeout.ms", "delivery.timeout.ms", "max.block.ms"}},
}

// exactClientKnobs is matched before clientKnobPatterns, by exact equality
// rather than substring. RubyGems names are bare identifiers, not import
// paths: the `pg` gem is a substring of Go's "github.com/jackc/pgx/v5", so
// a substring entry for it would silently hand pgx's caller ActiveRecord's
// knobs. Ruby's pooling knobs live in ActiveRecord's `database.yml`, which
// is where a reader has to go to change them; the per-connection timeouts
// belong to the driver gem itself.
var exactClientKnobs = map[string][]string{
	"pg":     {"pool", "checkout_timeout", "connect_timeout"},
	"mysql2": {"pool", "checkout_timeout", "connect_timeout", "read_timeout"},
	"redis":  {"timeout", "connect_timeout", "reconnect_attempts"},
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
	if knobs, ok := exactClientKnobs[client]; ok {
		return knobs
	}
	for _, p := range clientKnobPatterns {
		if strings.Contains(client, p.substr) {
			return p.knobs
		}
	}
	return nil
}

// buildCandidates turns one fault's detected dependency into the D-9
// candidate list (R-VER-4): library + known knobs, one entry per client
// library the target dependency was detected using — from
// detect.Dep.Clients (R-DET-5's lockfile/manifest scan) and, additionally,
// from internal/doctor's Audit findings for the same dependency (see
// candidatesFromAudit's doc comment for why this second source exists: a
// stdlib client like net/http never appears in a lockfile at all, so
// lockfile-sourced Clients alone systematically misses it). A target with
// no matching detect.Dep (an external host, or one detection simply never
// saw) and no audit finding for it produces no candidates — not a
// fabricated one.
func buildCandidates(f config.Fault, deps []detect.Dep, auditFindings []doctor.Finding, lang, sut string) []verdict.Candidate {
	source := manifestFor(lang)
	seen := map[string]bool{}
	var candidates []verdict.Candidate

	dep := depForTarget(deps, f.Target)
	if dep != nil {
		for _, client := range dep.Clients {
			if seen[client] {
				continue
			}
			seen[client] = true
			candidates = append(candidates, verdict.Candidate{
				Library: client,
				Source:  source,
				Knobs:   knobsFor(client),
			})
		}
	}

	depName := ""
	if dep != nil {
		depName = dep.Name
	} else {
		// depForTarget matches on Address; a dependency detect never built
		// a full Dep record for (e.g. a bare compose service name with no
		// recognized image, case 1's "dep") still has a name — the
		// hostname portion of the fault's own target — and internal/doctor
		// keys its Findings by DepName, not Address, so this is still
		// worth trying against the audit.
		depName = hostnameOf(f.Target)
	}
	keys := []string{depName}
	// TBD-10 attributes a standard-library client finding to the SUT
	// service whose source contains it — not to a dependency, because
	// R-AUD-5 cannot tell from source which host that client calls. Keying
	// the join on the fault target alone therefore never matched those
	// findings, and the knob `doctor` names on the same repo never reached
	// a verdict. The SUT is a second key, not a wildcard: a finding
	// attributed to any other service still does not join.
	if sut != "" && sut != depName {
		keys = append(keys, sut)
	}
	for _, key := range keys {
		for _, c := range candidatesFromAudit(key, auditFindings, source) {
			if seen[c.Library] {
				continue
			}
			seen[c.Library] = true
			candidates = append(candidates, c)
		}
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
// finding in a run is one of these.
//
// internal/verdict now carries this structurally (Finding.Unevaluated,
// Finding.Reason) rather than this package overloading Broke.Observed with
// a string prefix — the earlier approach ("not evaluated: ..." stuffed into
// Observed) let a rendered verdict show a comparison arrow and a measured-
// looking value next to an assertion that was never actually checked
// (`p(95)<2000 -> 0.583` reads as "0.583 was compared against 2000", not
// "this was never evaluated"). Broke is left entirely unset here: the
// renderer's INCONCLUSIVE path prints Reason instead, with no arrow and no
// value to misread. Broke.Assertion is still set — the renderer's `?
// <assertion> — not evaluated: <reason>` line needs to name which
// assertion this is — but Broke.Observed (and At/SustainedS) stay unset,
// per Finding.Unevaluated's own doc comment: nothing was measured, so there
// is nothing to print next to a comparison arrow that doesn't exist here.
func unevaluatedFinding(assertion, reason string) verdict.Finding {
	return verdict.Finding{
		Confidence:  verdict.Ambiguous,
		Unevaluated: true,
		Reason:      reason,
		Broke:       verdict.Broke{Assertion: assertion},
	}
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
	formatted, ok := lookupStat(m, statKey)
	if !ok && statKey == "rate" {
		// k6's own threshold syntax names a Rate-typed metric's aggregation
		// function "rate" (e.g. "rate<0.01"), but real k6 --summary-export
		// output names that same number "value" on the metric object
		// itself (confirmed against a real k6 run's actual JSON:
		// http_req_failed: {"value": 0, "passes": 0, "fails": 5, ...} — no
		// "rate" key at all). Fall back to it rather than report "not
		// measured" for data that is, in fact, right there.
		formatted, ok = lookupStat(m, "value")
	}
	if !ok {
		return "", false
	}
	if contains, _ := m["contains"].(string); contains == "time" {
		formatted += "ms"
	}
	return formatted, true
}

// lookupStat reads key directly off the metric object m — real k6
// --summary-export JSON puts every statistic (avg/min/med/max/p(NN)) there
// directly, not nested under a "values" sub-object, discovered running a
// real k6 container against a real SUT end to end (Task 7's R-DC2-3 load-
// path fix). The "values" fallback is kept for any summary shape that does
// nest them (e.g. an older or different export mode) rather than assuming
// the flat shape is the only one that will ever be seen.
func lookupStat(m map[string]any, key string) (string, bool) {
	raw, ok := m[key]
	if !ok {
		if values, ok2 := m["values"].(map[string]any); ok2 {
			raw, ok = values[key]
		}
	}
	if !ok {
		return "", false
	}
	v, ok := raw.(float64)
	if !ok {
		return "", false
	}
	return strconv.FormatFloat(v, 'f', -1, 64), true
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

// noFaultCandidateSource suffixes Candidate.Source for a fault-free finding
// (see buildCandidatesFromDetectedDeps) — the only place available in D-9's
// existing schema to carry the distinction between "the fault's target
// named this dependency" (tightly scoped) and "this is every client the
// run detected, offered because no fault narrowed it down" (the plausible
// set, not a diagnosis). Candidate has no separate per-candidate confidence
// field (internal/verdict, read-only for this task; deliberately minimal —
// R-VER-4 already forbids a file:line), and the finding's own Confidence
// already reads `ambiguous` for a fault-free breach (confidenceFor), but
// that label lives on the Finding, not the Candidate — a reader looking at
// Candidates alone (D-9's explain_failure surface) must not mistake this
// list for a tightly attributed one just because it's non-empty.
const noFaultCandidateSource = " (no active fault — detected client, not a diagnosis)"

// buildCandidatesFromDetectedDeps returns a candidate for every client
// library detect.System found, for a finding with no causing fault to
// narrow the search to one target (R-VER-4, D-9). E1 found this gap
// directly: two of six real detections were load-only defects (a
// connection pool exhausted under sustained load, a cache stampede on TTL
// expiry) — exactly the cases a candidate config surface is most useful
// for, and the client was detected perfectly in both, but attribute()
// previously only ever looked at active faults' targets, so a fault-free
// finding got nothing. Ordered by dependency address then client name for
// a deterministic verdict; a dependency detect.System found with no
// Clients at all (case 6's shape: an in-process, nobody's-library defect)
// contributes nothing, which is the honest answer, not a gap to pad.
func buildCandidatesFromDetectedDeps(deps []detect.Dep, auditFindings []doctor.Finding, lang string) []verdict.Candidate {
	if len(deps) == 0 {
		return nil
	}
	source := manifestFor(lang) + noFaultCandidateSource

	addresses := make([]string, 0, len(deps))
	byAddress := make(map[string]detect.Dep, len(deps))
	for _, dep := range deps {
		addresses = append(addresses, dep.Address)
		byAddress[dep.Address] = dep
	}
	sortStrings(addresses)

	var candidates []verdict.Candidate
	seen := map[string]bool{}
	for _, addr := range addresses {
		dep := byAddress[addr]
		clients := append([]string(nil), dep.Clients...)
		sortStrings(clients)
		for _, client := range clients {
			if seen[client] {
				continue
			}
			seen[client] = true
			candidates = append(candidates, verdict.Candidate{
				Library: client,
				Source:  source,
				Knobs:   knobsFor(client),
			})
		}
		for _, c := range candidatesFromAudit(dep.Name, auditFindings, source) {
			if seen[c.Library] {
				continue
			}
			seen[c.Library] = true
			candidates = append(candidates, c)
		}
	}
	return candidates
}

// candidatesFromAudit turns internal/doctor's Audit findings for depName
// into D-9 candidates (R-VER-4), in addition to (never instead of)
// whatever detect.Dep.Clients already offers.
//
// R-DET-1 forbids internal/detect (and this package) from reading source
// directly to discover a dependency's client library; R-AUD-5 explicitly
// permits internal/doctor's own bounded, table-driven construction-site
// inspection to do exactly that, for the small set of libraries its own
// table names. This consumes that result rather than re-implementing
// source reading here — doing so in this package would breach R-DET-1's
// bound a second time in a second place, which is the entire reason this
// function exists instead of a duplicate scan.
//
// This closes a gap E1 found directly (TBD-10): a fault-driven finding
// whose SUT client is Go's standard net/http (case 1, "HTTP client with no
// timeout" — the corpus's canonical resilience defect) was detected and
// correctly attributed, but could never carry Client.Timeout as the fix.
// net/http is stdlib and never appears in a go.mod require line, so
// detect.Dep.Clients (R-DET-5, lockfile-only) structurally cannot record
// it no matter how complete findings.go's own clientKnobPatterns is.
// internal/doctor's Audit is not bound by R-DET-1's lockfile-only rule for
// its own bounded inspection, so its Finding.Library can name net/http
// even when detect never could.
//
// Bound preserved, not widened: only a Library internal/doctor's own table
// already names ever reaches this function (Audit itself never invents
// one, per R-AUD-5/6) — this routes an already-bounded result into the
// candidate surface, it does not add any new source-reading capability of
// its own. auditFindings may be nil (most call sites, and every call site
// predating this fix): the honesty rules are unchanged either way — an
// unrecognized library still gets a name and no knobs (knobsFor), and a
// dependency the audit never reached (nil/empty auditFindings, or no
// finding matching depName) contributes nothing, not a gap to pad.
func candidatesFromAudit(depName string, auditFindings []doctor.Finding, source string) []verdict.Candidate {
	seen := map[string]bool{}
	var candidates []verdict.Candidate
	for _, af := range auditFindings {
		if af.DepName != depName || af.Library == "" || seen[af.Library] {
			continue
		}
		seen[af.Library] = true
		candidates = append(candidates, verdict.Candidate{
			Library: af.Library,
			Source:  source,
			Knobs:   knobsFor(af.Library),
		})
	}
	return candidates
}

// attribute fills in a finding's Cause and Candidates from the faults
// active during this run (R-VER-3, R-VER-4, D-9). Cause is only set when
// there is exactly one candidate fault — the same condition confidenceFor
// calls `correlated` — since attributing to a specific fault when >=2 were
// active would be exactly the fabrication `ambiguous` exists to prevent.
// Candidates, by contrast, are legitimately a list: D-4 defines `ambiguous`
// as ">=2 candidate causes", so every active fault's target contributes its
// own candidate config surface regardless of how many there are.
//
// With zero faults, there is no target to scope the search to at all — but
// that is not a reason to withhold candidates entirely (see
// buildCandidatesFromDetectedDeps's doc comment for why that was the E1
// gap): every detected client is offered instead, labeled as the plausible
// set rather than a single attributed cause.
func attribute(f *verdict.Finding, faults []config.Fault, deps []detect.Dep, auditFindings []doctor.Finding, lang, sut string) {
	if len(faults) == 1 {
		c := faults[0]
		f.Cause = &verdict.Cause{
			Fault:  c.Name,
			Target: c.Target,
			Inject: c.Inject,
			Window: faultWindow(c),
		}
	}
	if len(faults) == 0 {
		f.Candidates = buildCandidatesFromDetectedDeps(deps, auditFindings, lang)
		return
	}
	for _, fault := range faults {
		f.Candidates = append(f.Candidates, buildCandidates(fault, deps, auditFindings, lang, sut)...)
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
func evaluateThresholds(metrics map[string]any, faults []config.Fault, sys detect.System, auditFindings []doctor.Finding) ([]verdict.Passed, []verdict.Finding) {
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
			attribute(&finding, faults, sys.Deps, auditFindings, sys.Lang, sys.SUT)
			// R-VER-13/R-VER-14: a real causal chain, and the `caused`
			// confidence it earns, when (and only when) spans covering the
			// fault target can actually be read.
			applyTraceChain(&finding, sys)
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
func evaluatePromqlAsserts(asserts []config.AssertEntry, querier PromQuerier, faults []config.Fault, sys detect.System, auditFindings []doctor.Finding) ([]verdict.Passed, []verdict.Finding) {
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
		attribute(&finding, faults, sys.Deps, auditFindings, sys.Lang, sys.SUT)
		applyTraceChain(&finding, sys)
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
