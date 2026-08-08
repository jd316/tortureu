// Bencher — cross-commit trend tracking for a TortureU verdict
// (registry.yaml: tier delegate, when: always, how: "tortureu emit bencher",
// note "turns one-shot runs into a trend"). R-CLI-8 proposed.
//
// What this emits: a bash script that projects ONE verdict document
// (VERDICT.md §1) into Bencher Metric Format and hands it to `bencher run
// --adapter json --file`. It is not a benchmark runner and does not start a
// run — the verdict is the input, so this is what you run AFTER `tortureu
// run`, with that run's verdict.json.
//
// VERIFICATION STATUS (what was actually run on this host, 2026-08-09):
//
//   - VERIFIED against the real bencher CLI. The official installer
//     (https://bencher.dev/download/install-cli.sh) was run and produced
//     `bencher 0.6.11`. The script this file emits was then executed
//     verbatim (`bash trend.sh verdict.json`) against a synthetic verdict in
//     VERDICT.md §1's shape, and the real binary accepted the BMF document
//     it produced: `bencher run --project ... --adapter json --file bmf.json
//     --dry-run` exited 0 and echoed back the composed report, with the seven
//     benchmarks this projection yields (six http_req_duration statistics in
//     nanoseconds, one http_reqs rate as throughput). Reproduce with
//     TestBencher_AcceptedByRealBencherCLI (TORTUREU_EMIT_LIVE=1 go test
//     ./internal/emit/ -run TestBencher_AcceptedByRealBencherCLI).
//   - VERIFIED, the hard way: bencher 0.6.11 rejects an abbreviated commit
//     ("error: invalid value 'a3f19c2' for '--hash': Failed to validate git
//     hash") — the exact value VERDICT.md §1's own example carries. That is
//     why --hash is gated on a full 40-character hash below rather than
//     passed through.
//   - VERIFIED: the emitted jq program runs under the real jq and produces
//     valid BMF — checked by TestBencher_JqProgramProducesValidBMF, which is
//     part of the default gate (it skips only if jq is absent).
//   - NOT VERIFIED: nothing was uploaded to bencher.dev or to a self-hosted
//     Bencher. Every live check used `--dry-run`, which needs no token and
//     no server, so this file claims nothing about how a real project's
//     trend, alert or threshold behaves — only that the document is
//     accepted and the flags parse.
//   - NOT VERIFIED: no real TortureU verdict from a real `run` was fed
//     through it; the input used was a hand-written document in VERDICT.md
//     §1's shape with a metrics map in the form internal/k6's IngestSummary
//     produces.
//
// What it refuses to invent:
//
//   - The Bencher project. `--project` comes from BENCHER_PROJECT and has no
//     default: a guessed slug would push one repo's numbers into another
//     project's trend line, and an uploaded report cannot be un-uploaded.
//   - A measure. Bencher auto-creates any measure key it does not recognise
//     with the fallback unit "Measure (units)"
//     (bencher_json/src/project/measure/mod.rs), which would permanently
//     label a millisecond as unitless. So only the two built-in measures
//     whose units this data actually satisfies are emitted: `latency`
//     (nanoseconds) and `throughput` (operations/second). Every other
//     asserted metric is REPORTED as untranslated, never given a made-up
//     measure.
//   - A statistic. Nothing here picks p95-vs-p99 for you: every duration
//     statistic k6 reports for an asserted metric becomes its own benchmark,
//     so the trend tracks what the run measured rather than what we chose.
//   - A threshold policy. `--threshold-test`/`--threshold-upper-boundary`
//     appear only as commented examples with their real, valid values.
//     Picking a boundary is picking when to fail someone's CI.
//
// Fault translation: none, and that is not a gap. Bencher stores metrics; it
// injects nothing. Every fault in torture.yaml is reported per fault as
// untranslated with the emitter that does translate it (R-CLI-8).
package emit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/fault"
)

// bencherDurationMetrics are the k6 built-in metrics whose Trend statistics
// are milliseconds, and therefore the only ones that can honestly become
// Bencher's `latency` measure (unit: nanoseconds). k6's own metric reference
// is the source: these are its http_req_* timing metrics plus the two
// duration Trends it keeps outside that family. A custom metric a user
// defined in their own script is deliberately absent — we cannot know
// whether it counts bytes, retries or seconds, and a wrong unit in a trend
// is a wrong number in every future comparison.
var bencherDurationMetrics = map[string]bool{
	"http_req_duration":         true,
	"http_req_waiting":          true,
	"http_req_blocked":          true,
	"http_req_connecting":       true,
	"http_req_tls_handshaking":  true,
	"http_req_sending":          true,
	"http_req_receiving":        true,
	"iteration_duration":        true,
	"group_duration":            true,
	"grpc_req_duration":         true,
	"ws_connecting":             true,
	"ws_session_duration":       true,
	"browser_http_req_duration": true,
}

// bencherDurationStats are the statistic keys a k6 Trend metric carries that
// are themselves durations. `count` and `rate` are on the same object and are
// NOT durations, which is exactly why this is an allowlist rather than "every
// numeric key": multiplying a request count by 1e6 and filing it under
// latency would produce a plausible-looking, permanently wrong trend.
var bencherDurationStats = []string{"avg", "min", "med", "max", "p(50)", "p(90)", "p(95)", "p(99)"}

// bencherThroughputMetric is the one k6 metric whose `rate` statistic is
// operations per second, the exact unit of Bencher's built-in `throughput`.
const bencherThroughputMetric = "http_reqs"

const bencherHeader = `#!/usr/bin/env bash
# Generated by tortureu emit bencher. Requires: jq, and the bencher CLI
# (curl --proto '=https' --tlsv1.2 -sSfL https://bencher.dev/download/install-cli.sh | sh).
#
#   bash this-script.sh <path to a verdict.json>
#
# This turns ONE run's verdict (VERDICT.md §1) into Bencher Metric Format and
# reports it, so repeated runs on successive commits become a trend. It runs
# nothing and measures nothing itself: the verdict is its input.
#
# REQUIRED, with no default: BENCHER_PROJECT. A guessed project slug uploads
# this repo's numbers into someone else's trend, and that cannot be undone.
# Credentials are never emitted — export BENCHER_API_KEY (or BENCHER_API_TOKEN)
# yourself, or set TORTUREU_BENCHER_DRY_RUN=1 to compose the report and send
# nothing.
#
# Optional: TORTUREU_BMF_OUT (where to write the BMF, default ./bencher-bmf.json),
# TORTUREU_BENCHER_BMF_ONLY=1 (write the BMF and stop), BENCHER_BRANCH,
# BENCHER_TESTBED, BENCHER_HOST (a self-hosted Bencher) — all passed through
# by the CLI's own environment variables, not re-derived here.
#
# at:/for: are NOT scheduled by this script — this is a delegate-tier handoff
# (real output, separate timing, registry.yaml).
set -euo pipefail
`

// Bencher emits the verdict -> Bencher Metric Format handoff described in
// this file's header. It consults only torture.yaml (registry.yaml has it
// as when: always), so sys is unused and may be nil.
func Bencher(cfg *config.Config, _ *detect.System) (string, error) {
	tracked, untranslated := bencherPartitionAsserts(cfg)

	var faultNotes strings.Builder
	for _, f := range cfg.Faults {
		if _, err := fault.Translate(f); err != nil {
			return "", fmt.Errorf("emit bencher: %w", err)
		}
		faultNotes.WriteString(atComment(f))
		faultNotes.WriteString(skipComment("bencher", f,
			"bencher stores metrics across commits and injects nothing; use \"tortureu emit pumba\", \"netem\" or \"iptables\" to inject this fault"))
	}

	var b strings.Builder
	b.WriteString(bencherHeader)

	b.WriteString(`
verdict="${1:-${TORTUREU_VERDICT:-}}"
if [ -z "$verdict" ]; then
  echo "usage: $0 <verdict.json>   (or set TORTUREU_VERDICT)" >&2
  exit 2
fi
if [ ! -f "$verdict" ]; then
  echo "tortureu emit bencher: no such verdict document: $verdict" >&2
  exit 2
fi
if [ -z "${BENCHER_PROJECT:-}" ]; then
  echo "tortureu emit bencher: BENCHER_PROJECT is required and has no default." >&2
  echo "  torture.yaml carries no Bencher project, and guessing one would report this" >&2
  echo "  repo's numbers into another project's trend. Set it and re-run." >&2
  exit 2
fi
for tool in jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "tortureu emit bencher: $tool not found on PATH" >&2; exit 2; }
done

# R-VER-2: status=error means TortureU broke, status=aborted means the run
# never started (DC-2 refused, or reset failed). Neither carries a measurement
# of the system under test, so neither may enter a trend — a harness failure
# recorded as a datapoint is a lie that persists across every later comparison.
status=$(jq -r '.status // "unknown"' "$verdict")
case "$status" in
  pass|fail) ;;
  error|aborted)
    echo "tortureu emit bencher: verdict status is \"$status\" — no measurement of the system" >&2
    echo "  under test exists in it, so nothing is reported. This is not an error." >&2
    exit 0 ;;
  *)
    echo "tortureu emit bencher: verdict status \"$status\" is not one of pass|fail|error|aborted" >&2
    exit 2 ;;
esac

bmf="${TORTUREU_BMF_OUT:-./bencher-bmf.json}"
`)

	b.WriteString("\n# The projection, as a jq program. Benchmark names carry the scenario and the\n" +
		"# exact statistic, so two statistics of one metric are two trend lines rather\n" +
		"# than one line whose meaning depends on which we happened to pick.\n")
	b.WriteString("jq '\n")
	b.WriteString(bencherJqPrelude)
	b.WriteString(bencherJqBody(tracked))
	b.WriteString("' \"$verdict\" > \"$bmf\"\n")

	b.WriteString(`
if [ "$(jq -r 'length' "$bmf")" = "0" ]; then
  echo "tortureu emit bencher: the verdict carries none of the metrics torture.yaml asserts;" >&2
  echo "  an empty BMF document would create an empty trend, so nothing is reported." >&2
  exit 0
fi
echo "tortureu emit bencher: wrote $bmf" >&2
if [ -n "${TORTUREU_BENCHER_BMF_ONLY:-}" ]; then
  exit 0
fi

# --hash is VERDICT.md's own commit field (§1), the anchor §12's trend
# tracking is for. Two real constraints, both found by running this:
#   1. bencher 0.6.11 VALIDATES the value as a full 40-character git hash and
#      exits 2 on anything shorter — VERDICT.md's own example ("a3f19c2") is
#      rejected. An abbreviated hash is therefore passed over with a warning
#      rather than silently mangled into something that would parse.
#   2. no producer in this codebase sets verdict.commit yet, so in practice
#      this is usually absent. Absent means omitted, never invented: a report
#      filed against the wrong commit is worse than one filed against none.
hash_args=()
commit=$(jq -r '.commit // ""' "$verdict")
if [ -n "$commit" ]; then
  if printf '%s' "$commit" | grep -Eq '^[0-9a-fA-F]{40}$'; then
    hash_args=(--hash "$commit")
  else
    echo "tortureu emit bencher: verdict commit \"$commit\" is not a full 40-character git" >&2
    echo "  hash, which is what bencher --hash requires; reporting without it." >&2
  fi
fi
dry_args=()
if [ -n "${TORTUREU_BENCHER_DRY_RUN:-}" ]; then
  dry_args=(--dry-run)
fi

# --adapter json is what reads a BMF document (bencher's other adapters parse a
# specific tool's output; "magic", the default, would sniff). --file takes the
# document with no command to run, which is the shape documented for custom
# benchmarks.
bencher run \
  --project "$BENCHER_PROJECT" \
  --adapter json \
  --file "$bmf" \
  "${hash_args[@]}" \
  "${dry_args[@]}"

# THRESHOLDS ARE NOT SET HERE, deliberately. Bencher can fail this command when
# a measure regresses, but choosing the boundary is choosing when someone's CI
# goes red, and torture.yaml says nothing about it. The real flags and their
# real accepted values, for when you decide:
#
#   --threshold-measure latency \
#   --threshold-test t_test \        # static|percentage|z_score|t_test|log_normal|iqr|delta_iqr
#   --threshold-max-sample-size 64 \
#   --threshold-upper-boundary 0.95 \
#   --err                            # fail this command on an alert
#
# Verified against bencher 0.6.11's own --help; not exercised, because no
# threshold was chosen.
`)

	if untranslated.Len() > 0 {
		b.WriteString("\n# assertions in torture.yaml that this emit does NOT translate\n" +
			"# (listed, never dropped — R-CLI-8):\n")
		b.WriteString(untranslated.String())
	}
	if faultNotes.Len() > 0 {
		b.WriteString("\n# faults declared in torture.yaml that this emit does NOT translate\n" +
			"# (listed, never dropped — R-CLI-8):\n")
		b.WriteString(faultNotes.String())
	}
	return b.String(), nil
}

// bencherTrack is one asserted metric this emit can carry into a built-in
// Bencher measure, with the conversion that makes its unit true.
type bencherTrack struct {
	metric  string
	measure string   // "latency" or "throughput" — nothing else exists here
	stats   []string // statistic keys on the k6 metric object
	scale   string   // jq multiplier turning k6's unit into the measure's
}

// bencherPartitionAsserts splits torture.yaml's assert block into the metrics
// that map onto a built-in measure and a report of everything that does not.
// promql: and sql: entries are assertions, not k6 metrics, so they are named
// with the reason rather than silently skipped (R-CLI-8).
func bencherPartitionAsserts(cfg *config.Config) ([]bencherTrack, *strings.Builder) {
	var tracked []bencherTrack
	var notes strings.Builder
	seen := map[string]bool{}

	for _, entry := range cfg.Assert {
		keys := make([]string, 0, len(entry))
		for k := range entry {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if seen[k] {
				continue
			}
			seen[k] = true
			switch {
			case k == "promql":
				fmt.Fprintf(&notes, "# assert promql: %q: not translated — a PromQL assertion is evaluated against your\n"+
					"#   Prometheus, not measured by the run, so the verdict carries no number for it.\n"+
					"#   \"tortureu emit dashboard\" plots these.\n", entry[k])
			case k == "sql":
				fmt.Fprintf(&notes, "# assert sql: %q: not translated — a SQL invariant is a boolean about data, not a\n"+
					"#   metric with a value to trend. \"tortureu emit soda\" turns these into real checks.\n", entry[k])
			case k == bencherThroughputMetric:
				tracked = append(tracked, bencherTrack{
					metric: k, measure: "throughput", stats: []string{"rate"}, scale: "1",
				})
			case bencherDurationMetrics[k]:
				tracked = append(tracked, bencherTrack{
					metric: k, measure: "latency", stats: bencherDurationStats, scale: "1000000",
				})
			default:
				fmt.Fprintf(&notes, "# assert %q: not translated — Bencher's built-in measures are latency\n"+
					"#   (nanoseconds) and throughput (operations/second), and this metric is neither.\n"+
					"#   Inventing a measure key would have Bencher create it with the placeholder unit\n"+
					"#   \"Measure (units)\", which mislabels the number in every future comparison.\n", k)
			}
		}
	}
	return tracked, &notes
}

// bencherJqPrelude defines the two helpers the generated program uses. `stat`
// reads a statistic from either shape internal/k6's IngestSummary can hand
// on: k6 --summary-export puts statistics directly on the metric object,
// handleSummary() nests them under "values".
const bencherJqPrelude = `def stat($m; $s):
  (.metrics[$m] // {}) | (if has($s) then .[$s] else ((.values? // {})[$s]) end);
def entry($name; $measure; $value; $scale):
  if ($value | type) == "number"
  then {($name): {($measure): {"value": ($value * $scale)}}}
  else {} end;
`

// bencherJqBody renders the reduce over every tracked (metric, statistic).
// An absent statistic contributes nothing rather than a null datapoint.
func bencherJqBody(tracked []bencherTrack) string {
	if len(tracked) == 0 {
		return "{}\n"
	}
	var b strings.Builder
	b.WriteString("[\n")
	for i, t := range tracked {
		for j, s := range t.stats {
			fmt.Fprintf(&b, `  entry("\(.scenario) %s %s"; %q; stat(%q; %q); %s)`,
				t.metric, s, t.measure, t.metric, s, t.scale)
			if !(i == len(tracked)-1 && j == len(t.stats)-1) {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("] | add // {}\n")
	return b.String()
}

func init() {
	// needsSystem: false. registry.yaml lists bencher as when: always, and
	// everything here comes from torture.yaml's assert block plus the
	// verdict the emitted script reads at run time. Asking for detection
	// would make a repo whose compose file cannot be parsed unable to track
	// a trend it already has the numbers for.
	Register("bencher", Bencher, false)
}
