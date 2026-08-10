#!/bin/bash
# run_case.sh [case-dir]
#
# With no argument: runs the whole E1 corpus (every evals/corpus/* case),
# through the REAL `tortureu run` binary and the real internal/run path --
# no reimplementation, no shortcuts. This is the `make eval` target.
#
# With a case directory argument: runs just that one case (useful while
# iterating on a single fixture).
#
# Exit code: non-zero if the corpus couldn't be driven at all, OR if case
# 8 -- the control, which must never produce a finding -- produced one.
# That single check is the E1 launch gate BENCHMARKS.md describes.
#
# Docker is torn down on every exit path, including failure, per the E1
# task brief's warning about internal/run's past stack-leak bug: each
# case's compose stack (base file + whatever topology overlay
# ComposeTopologyApplier wrote) is brought down unconditionally before this
# script exits, success or not.
set -u

EVAL_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$EVAL_ROOT/.." && pwd)"
RESULTS_DIR="$EVAL_ROOT/results"
export TMPDIR="${TMPDIR:-$EVAL_ROOT/tmp}"
mkdir -p "$TMPDIR" "$RESULTS_DIR"

# The real tortureu binary. Building it here (rather than requiring a
# pre-built copy on PATH) is what makes `make eval` a single command.
TORTUREU_BIN="$TMPDIR/tortureu"
GO_BIN="${GO_BIN:-go}"
if ! command -v "$GO_BIN" >/dev/null 2>&1 && [ -x "$HOME/.local/go/bin/go" ]; then
  GO_BIN="$HOME/.local/go/bin/go"
fi
echo "==> building tortureu"
if ! "$GO_BIN" build -o "$TORTUREU_BIN" "$REPO_ROOT/cmd/tortureu"; then
  echo "run_case.sh: go build failed" >&2
  exit 2
fi

# Case 1/2/5's "dep" service is a pre-built image (image:, not build:) so
# R-DET-8's "build: is the SUT" rule can't misidentify it -- these need a
# `docker build` before `docker compose up` will find them.
declare -A PREBUILT_IMAGES=(
  [case1-no-timeout]="e1-case1-dep:latest:dep"
  [case2-retry-storm]="e1-case2-dep:latest:dep"
  [case5-no-circuit-breaker]="e1-case5-dep:latest:dep"
)

# Case 4's promql: assert and duplicate fault need the orchestrator to
# reach a live Prometheus and a live broker admin API, both moved onto the
# internal:true SUT network by DC-2 enforcement same as everything else.
# internal/run.fallbackTransport (a direct call first, falling back to a
# Docker network-namespace tunnel on failure) makes -broker-url/-prom-url
# work against the compose service name directly -- no host-published
# port, no standalone-container workaround needed any more.
declare -A EXTRA_ARGS=(
  [case4-nonidempotent-consumer]="-broker-url http://redpanda:8082 -prom-url http://prometheus:9090"
)

# Case 9 names its trace backend explicitly. It has to: DC-2 enforcement
# leaves the stack's Jaeger with no published host port, so the default
# probe (localhost:16686) finds nothing -- internal/run reaches
# `jaeger:16686` through the same in-stack transport it uses for
# Prometheus (R-VER-13). This is an environment variable rather than a flag
# because the endpoint is a property of the machine, not of the scenario.
declare -A EXTRA_ENV=(
  [case9-multi-fault-traced]="TORTUREU_TRACE_URL=http://jaeger:16686"
)

run_one_case() {
  local case_dir="$1"
  local case_name
  case_name="$(basename "$case_dir")"
  local overlay="$TMPDIR/tortureu-topology-overlay.yaml"
  # evals/results/caseN.json, the historical naming (case_name is
  # "caseN-slug").
  local out="$RESULTS_DIR/$(echo "$case_name" | grep -oE '^case[0-9]+').json"
  local errout="${out%.json}.stderr"

  echo "==> $case_name"

  cleanup() {
    if [ -f "$case_dir/docker-compose.yml" ]; then
      if [ -f "$overlay" ]; then
        (cd "$case_dir" && docker compose -f docker-compose.yml -f "$overlay" down -v) >/dev/null 2>&1
      else
        (cd "$case_dir" && docker compose down -v) >/dev/null 2>&1
      fi
    fi
    # Belt and suspenders: `docker compose down` has been observed (this
    # eval's own finding) to skip a service the R-EXE-20 overlay disabled
    # via a compose profile, leaking its container -- and, worse, the host
    # port it published, breaking the *next* case that wants the same
    # port. A project-name-prefixed sweep catches anything `down` missed.
    local project
    project="$(basename "$case_dir")"
    local stragglers
    stragglers="$(docker ps -aq --filter "name=^${project}-" 2>/dev/null)"
    if [ -n "$stragglers" ]; then
      docker rm -f $stragglers >/dev/null 2>&1
    fi
    docker network ls --format '{{.Name}}' 2>/dev/null | grep -E '^tortureu_(sut|egress)$' | xargs -r docker network rm >/dev/null 2>&1
    # The Reset step's own plain `docker compose up` (before the topology
    # overlay ever applies) creates compose's default "<project>_default"
    # network; once the overlay's explicit networks take over, nothing
    # ever names it again for `down` to remove alongside the merged files.
    docker network rm "${project}_default" >/dev/null 2>&1
    rm -f "$overlay"
  }
  trap cleanup RETURN

  local prebuilt="${PREBUILT_IMAGES[$case_name]:-}"
  if [ -n "$prebuilt" ]; then
    local image="${prebuilt%%:*}"
    local rest="${prebuilt#*:}"
    local tag="${rest%%:*}"
    local subdir="${rest##*:}"
    if ! docker image inspect "$image:$tag" >/dev/null 2>&1; then
      echo "    building $image:$tag from $case_dir/$subdir"
      docker build -q -t "$image:$tag" "$case_dir/$subdir" >/dev/null
    fi
  fi

  rm -f "$overlay"
  local extra="${EXTRA_ARGS[$case_name]:-}"
  local extra_env="${EXTRA_ENV[$case_name]:-}"
  (cd "$case_dir" && env $extra_env "$TORTUREU_BIN" run -config torture.yaml -json $extra) >"$out" 2>"$errout"
  local rc=$?
  local status findings
  status="$(python3 -c "import json,sys
try:
    print(json.load(open('$out')).get('status','?'))
except Exception:
    print('unreadable')" 2>/dev/null)"
  findings="$(python3 -c "import json,sys
try:
    print(len(json.load(open('$out')).get('findings',[])))
except Exception:
    print('?')" 2>/dev/null)"
  echo "    status=$status findings=$findings (exit $rc)"
  echo "$case_name:$status:$findings"
}

RESULTS_LINE_FILE="$TMPDIR/run_case_results.txt"
: > "$RESULTS_LINE_FILE"

if [ $# -ge 1 ]; then
  run_one_case "$(cd "$1" && pwd)" | tee -a "$RESULTS_LINE_FILE"
else
  for d in "$EVAL_ROOT"/corpus/case*/; do
    run_one_case "${d%/}" | tee -a "$RESULTS_LINE_FILE"
  done
fi

# The launch gate: case 8 (the control) MUST actually run and produce zero
# findings.
#
# "Zero findings" alone is not the gate, and used to be: an aborted run has
# zero findings because it never measured anything, so a corpus that failed
# to start at all printed "OK: case 8 produced 0 findings" and exited 0.
# That is the same false green E1 exists to detect, in the harness that
# detects it. A missing case8 line passed just as silently.
#
# So the control must be status=pass, and every other case must have
# produced a readable verdict — an all-aborted corpus is a harness failure,
# never evidence.
gate_rc=0

# The control requirement applies to a whole-corpus run. A single-case
# invocation (`run_case.sh <dir>`) legitimately has no case8 line, and
# demanding one there made every single-case run fail.
case8_line="$(grep '^case8' "$RESULTS_LINE_FILE" | tail -1)"
if [ -z "$case8_line" ]; then
  if [ $# -eq 0 ]; then
    echo "FAIL: no case8 result at all -- the control never ran, so nothing was proven" >&2
    gate_rc=1
  fi
else
  case8_status="$(echo "$case8_line" | cut -d: -f2)"
  case8_findings="$(echo "$case8_line" | cut -d: -f3)"
  if [ "$case8_status" != "pass" ]; then
    echo "FAIL: case 8 (control) status=$case8_status -- a control that did not run clean cannot certify anything" >&2
    gate_rc=1
  elif [ "$case8_findings" != "0" ]; then
    echo "FAIL: case 8 (control) produced $case8_findings finding(s) -- E1's launch gate requires zero" >&2
    gate_rc=1
  else
    echo "OK: case 8 (control) ran clean and produced 0 findings"
  fi
fi

# Only meaningful for a whole-corpus run; a single-case invocation names its
# own case and is not expected to cover the rest.
if [ $# -eq 0 ]; then
  # Only "<case>:<status>:<findings>" lines are results. The file also
  # carries tee'd progress lines ("==> case1", "    status=..."), so a bare
  # line count reported 24 for an 8-case corpus.
  results="$(grep -E '^case[0-9]+[^:]*:[a-z?]+:' "$RESULTS_LINE_FILE" || true)"
  total="$(printf '%s\n' "$results" | grep -c . || true)"
  unusable="$(printf '%s\n' "$results" | grep -cE ':(aborted|error|unreadable|\?)$|:(aborted|error|unreadable|\?):' || true)"
  echo "corpus: $total case(s), $unusable did not produce a usable verdict"
  if [ "$unusable" -gt 0 ]; then
    echo "FAIL: $unusable case(s) aborted or were unreadable -- these are harness/environment failures, not results, and must not be scored or published" >&2
    gate_rc=1
  fi
fi

exit $gate_rc
