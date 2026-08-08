#!/bin/bash
# run_case.sh <case-dir> <tortureu-binary>
#
# Drives one E1 corpus case through the REAL `tortureu run` path: no
# reimplementation, no shortcuts around internal/run. Tears down every
# Docker resource this invocation created on every exit path — including
# failure — per the E1 task brief's explicit warning about internal/run's
# past stack-leak bug.
#
# Prerequisite: evals/bin/k6 (a Docker-backed stand-in for a native k6
# install — see that file's own header) must be on PATH ahead of any real
# k6, since k6 itself is not installed in this environment.
set -u

CASE_DIR="$1"
TORTUREU_BIN="${2:-/tmp/e1-tortureu}"
EVAL_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export TMPDIR="${TMPDIR:-$EVAL_ROOT/tmp}"
mkdir -p "$TMPDIR"
export PATH="$EVAL_ROOT/bin:$PATH"

OVERLAY="$TMPDIR/tortureu-topology-overlay.yaml"

cleanup() {
  if [ -f "$CASE_DIR/docker-compose.yml" ]; then
    if [ -f "$OVERLAY" ]; then
      (cd "$CASE_DIR" && docker compose -f docker-compose.yml -f "$OVERLAY" down -v) >/dev/null 2>&1
    else
      (cd "$CASE_DIR" && docker compose down -v) >/dev/null 2>&1
    fi
  fi
  rm -f "$OVERLAY"
}
trap cleanup EXIT

rm -f "$OVERLAY"
cd "$CASE_DIR" || exit 2
"$TORTUREU_BIN" run -config torture.yaml -json
