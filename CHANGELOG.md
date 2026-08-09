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
- Compose files are resolved by the Compose Specification's own precedence — `compose.yaml`,
  `compose.yml`, `docker-compose.yaml`, `docker-compose.yml` — so a repo using the canonical name
  needs no flag.
- Multi-fault attribution from traces: with several faults active, the one whose target actually
  degraded becomes the named cause. Two degraded targets, or none, stay `ambiguous`.

### Fixed
- `init` failed on the first command against most real repositories: only `docker-compose.yml` was
  resolved, and of the 40 examples in Docker's own `awesome-compose`, 37 use `compose.yaml` and
  none use `docker-compose.yml`.
- `doctor` reported k6 as a missing prerequisite. `run` never uses a host k6 — DC-2's topology
  leaves the SUT with no published port, so it always runs the pinned container. The first command
  a new user typed told them to install a tool the tool does not touch.
- A failed reset would not say why: the shell command's output was discarded, so a real first-run
  abort rendered `reset: failed` with `"error": null`.
- An empty `target.base_url` produced a full load of failing requests and a verdict reporting
  `http_req_failed` = 1.0 — a finding that was entirely an artefact of a missing config field. It
  is now refused before reset and before load.
- Trace ingestion used a host HTTP client and so could never reach a Jaeger inside the
  DC-2-isolated stack, meaning no real run could build a causal chain.
- The trace degradation gate was a bare 2x ratio, so an undisturbed dependency (587µs to 1.3ms)
  read as a second candidate cause beside the real one (817µs to 3002ms). It now also requires an
  absolute step.
- Observed metric values were emitted as raw floats without units (`3003.2139021999997`) where
  `VERDICT.md` specifies `4218ms`, and the human rendering printed the full 40-character commit
  hash where the same document abbreviates it.
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
- Attribution is 5/5 on findings from runs that injected a fault, and 5/9 over every finding: three
  corpus cases inject no fault at all, so there is no cause to name and `ambiguous` is the correct
  verdict rather than a miss.
- `caused` requires traces. A repo without OpenTelemetry tops out at `correlated`; one corpus case
  (case 9) is instrumented and does reach `caused`.
- Harness overhead (B2) is below the benchmark's own run-to-run variance; no signed figure is
  claimed.
- `TBD-5` stays open, but no longer on upstream: `grafana/k6-summary` has shipped as `1.0.0`. k6
  emits that shape only behind `--new-machine-readable-summary` (opt-in even in v2.1.0), and the
  pinned `grafana/k6:0.54.0` cannot emit it at all, so adopting it now needs a k6 major-version
  bump — which would move the phase markers, threshold recomputation and every B1 fidelity number
  measured against 0.54.0, and so belongs in its own change with its own re-measurement.
