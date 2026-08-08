# Changelog

Notable changes. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
this project will use [semantic versioning](https://semver.org/) from its first tag.

Exit codes 0–4 (`VERDICT.md` §2) and the verdict document's field names are the compatibility
surface agents parse — those are what a major version protects.

## [Unreleased]

Everything below is in `main` and has not yet been tagged. There is no published release,
archive or image; `tortureu init --ci` says so and fails its install step rather than emitting a
download that 404s.

### Added
- Ten verbs, all real: `init`, `run`, `doctor`, `mcp`, `smoke`, `check`, `emit`, `capture`,
  `replay`, `trend`. `mcp` speaks JSON-RPC 2.0 over stdio.
- `emit` covers 28 delegate-tier targets, each registering itself so adding one touches no shared
  file. All 155 tools in `registry.yaml` are reachable from the CLI.
- Trace ingestion (`internal/trace`, Jaeger query API): a finding reaches `caused` confidence with
  a real fault→symptom chain, clamped by the observability ceiling detection reports.
- Drive-tier co-execution on the run clock: `run --db-load` (pgbench) and `run --fuzz`
  (schemathesis), both able to reach a SUT or database isolated on a DC-2 `internal: true` network
  by joining its container namespace.
- `sql:` assertions are evaluated against PostgreSQL and MySQL (`-sql-url`). A `sql:` expression is
  a violation **count**; the invariant holds iff it returns `0`.
- `trend`: a local append-only JSONL verdict store with per-metric deltas and NEW/GONE findings,
  anchored on the run's git commit. `run --trend` records in one step.
- `check contracts`: breaking-change detection via oasdiff and buf, against a git ref or a file.
- `capture --engine keploy`, and cassettes now carry `call_ns`/`return_ns` so a recorded history
  has real concurrency.
- Release mechanics: goreleaser config, Dockerfile, CI and release workflows, `CONTRIBUTING.md`.

### Fixed
- `check contracts` reported a broken API as passing: `oasdiff breaking` exits 0 even when it finds
  breaking changes (it needs `--fail-on ERR`), and `buf breaking` exits 100, not 1 — so a real
  finding was relayed as a tool error, and a failed baseline as a confident finding.
- The standard-library HTTP timeout knob could never reach a verdict: audit findings are attributed
  to the SUT service, but candidates were looked up by the fault target's hostname alone.
- `emit ghz` generated a script that panicked on its own first command (`--load-start=0`).
- `emit fio` wrote scratch files into a live PostgreSQL data directory, and let a failed cleanup
  report failure for a successful run.
- `emit locust` produced a closed-model locustfile whose rate collapsed as latency rose; it now
  dispatches arrivals on an absolute tick schedule and holds rate at 2000 ms response time.
- `emit chaosmesh` produced CRDs a live admission webhook rejected (over-long names), and silently
  dropped `for:` on one-shot pod-kill.
- The verdict carried neither the observability ceiling nor the commit anchor; both fields existed
  and no producer wrote them.
- The E1 eval's launch gate certified a corpus in which every case aborted — an aborted run has
  zero findings, which is what the gate tested for.

### Known limitations
- Attribution names a single causing fault in 4 of 7 corpus cases; with several faults active it
  returns `ambiguous` rather than guessing.
- No corpus case runs OpenTelemetry, so `caused` is verified against a live Jaeger but not scored
  by the eval.
- Harness overhead (B2) is below the benchmark's own run-to-run variance; no signed figure is
  claimed.
- `TBD-5` stays open pending `grafana/k6-summary` leaving work-in-progress upstream.
