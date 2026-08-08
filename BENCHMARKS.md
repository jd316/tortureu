# Benchmarks & Evals

A torture tool that is not itself measured is an opinion. This file defines what we measure,
how, and what we publish. Everything here is reproducible by a third party — that is the point.

**Rule:** we never publish a number we cannot regenerate with one command from a public repo.

---

## Why a testing tool needs its own evals

Three claims live at the centre of TortureU. Each is falsifiable, so each gets a benchmark:

| Claim | Benchmark | Fails if |
|---|---|---|
| Faults are what we say they are | **B1 fault fidelity** | injected 300ms lands as 280ms or 420ms |
| The harness doesn't distort what it measures | **B2 overhead** | proxy adds meaningful p99 to a clean run |
| The verdict names the *right* cause | **E1 attribution** | it blames Redis when Postgres was degraded |

B1 and B2 are table stakes — a measuring instrument must be accurate and low-perturbation.
**E1 is the actual product claim** and the only one competitors can't trivially match.

---

## B1 — Fault fidelity

*Does the fault we schedule match the fault that occurs?*

For each `inject:` verb, drive a known-good echo service and measure the delta between
requested and observed effect. This is now real: `benchmarks/b1` drives a real
docker-compose stack (a `client` and an `echo` TCP service) through the actual production
path — `ComposeTopologyApplier.Apply` + `egress.BuildTopology` + `fault.Translate` +
`fault.Manager` + `ToxiproxyApplier`/`DockerApplier`/`CombinedApplier`, exactly as
`internal/run`'s own Docker-backed tests exercise it — and measures the client-observed
effect. `make bench` runs it; results land in `benchmarks/results/<date>-<commit>.json`.

**Design spec** (the table this benchmark is built to fill in):

| Fault | Requested | Measured | Tolerance |
|---|---|---|---|
| `latency: 300ms` | +300ms | p50 delta at the client | ±10ms |
| `jitter: 50ms` | σ=50ms | stddev of delta | ±15% |
| `bandwidth: 1mbps` | 1 Mbps | bytes/sec through the proxy | ±5% |
| `down` | connection refused | error class at client | exact |
| `pause` (SIGSTOP) | no response, conn held open | client sees timeout not RST | exact |
| `kill` (SIGKILL) | conn reset | client sees RST | exact |
| `cpu: 90%` | 90% of quota | cgroup cpu.stat | ±5% |

**Platform:** Linux 7.0.0-29-generic, Docker 29.5.3, AMD Ryzen 7 5800H (16 cores), cgroup v2.
Measured `2026-08-08T10:15:52Z` at commit `07acb03`
([full JSON](benchmarks/results/2026-08-08-07acb03.json)). Not yet measured on macOS or
Docker Desktop — those rows do not exist yet, and BENCHMARKS.md does not claim they do.

**Results — primary measurement: exactly what a human-authored `torture.yaml` produces
today** (unit-suffixed strings, decoded by `yaml.v3` as literal Go strings, and passed
by `internal/fault/translate.go` straight into Toxiproxy/Docker's numeric fields with
**no unit parsing anywhere in `internal/config` or `internal/fault`** — see the finding
below the table):

| Fault | Requested | Measured | Tolerance | Verdict |
|---|---|---|---|---|
| `latency: 300ms` | +300ms | Toxiproxy rejected the toxic: `400 bad request body: json: cannot unmarshal string into Go struct field LatencyToxic.attributes.jitter of type int64` | ±10ms | **MISS** |
| `jitter: 50ms` | σ=50ms | same rejection (one combined `latency`+`jitter` toxic call) | ±15% | **MISS** |
| `bandwidth: 1mbps` | 1 Mbps | Toxiproxy rejected the toxic: `400 bad request body: json: cannot unmarshal string into Go struct field BandwidthToxic.attributes.rate of type int64` | ±5% | **MISS** |
| `down` | connection refused | client saw `ConnectionRefusedError` | exact | **PASS** |
| `pause` (SIGSTOP-equivalent freeze) | no response, conn held open | 5/5 post-fault attempts timed out on an open connection, no RST | exact | **PASS** |
| `kill` (SIGKILL) | conn reset | connection closed gracefully (EOF, no exception) rather than an RST-triggering error | exact | **MISS** |
| `cpu: 90%` | 90% of quota | cgroup v2 `cpu.stat` measured ~403–412% of one core across runs (4 unthrottled stress-ng workers at 100%, not 90%) | ±5% | **MISS** |

Four of seven rows miss, and one (`down`) is the only network-verb row that passes as
written — because `down` carries no numeric modifier for translate.go to mishandle.

**Secondary measurement, clearly separate: if translate.go's unit gap did not exist**
(this harness converts units itself and hands `fault.Translate` already-numeric input,
to see whether the underlying Toxiproxy/Docker mechanism would work at all):

| Fault | Requested (numeric) | Measured | Tolerance | Verdict |
|---|---|---|---|---|
| `latency` | +300ms | p50 delta = 300.97ms (n=40, stddev 0.74ms) | ±10ms | **PASS** |
| `jitter` | σ=50ms | stddev of delta = 16.76ms (n=40) | ±15% | **MISS** — not a translate.go defect: Toxiproxy's own jitter adds a *uniform* random offset in `[-jitter,+jitter]`, not Gaussian noise with stddev=jitter (a plain uniform(-50,50) alone has stddev ≈28.9ms), and with `latency:0` any negative computed delay clamps to zero, lowering the effective stddev further still. |
| `bandwidth` | 1 Mbps == 122 KB/s (harness-computed conversion; translate.go still does not do this conversion) | 120,863 bytes/sec | ±5% | **PASS** |

**Findings:**

1. **Confirmed instrument-fidelity gap in `internal/fault/translate.go`.**
   `translateToxic` copies `f.Inject["latency"]`/`["jitter"]`/`["bandwidth"]` verbatim into
   Toxiproxy's numeric `Attributes` map with no unit-string parsing anywhere upstream
   (`internal/config` decodes `latency: 300ms` as the Go string `"300ms"` and never
   converts it). Concretely: `internal/fault/translate.go`'s `translateToxic`,
   `t.Attributes["latency"] = f.Inject["latency"]` — copies the raw YAML value with no
   unit conversion. The exact fault shape `torture.example.yaml` itself ships
   (`inject: { latency: 300ms, jitter: 50ms }`) makes Toxiproxy's real HTTP API reject the
   toxic outright. A user who writes exactly what the project's own reference config and
   this same file's table show gets a **hard failure at apply time**, not a slightly-off
   fault.
2. **Confirmed gap in `internal/fault/translate.go`'s `cpu` verb.** `translateDocker`'s
   `"cpu"` case sets `d.Args["amount"] = f.Inject["cpu"]` (e.g. `"90%"`), but
   `DockerApplier.ApplyDocker`'s `"stress"` case never reads `Args["amount"]` at all — it
   only reads `Args["workers"]` and runs `stress-ng --cpu <workers>` with no load
   percentage, and there is no cgroup CPU quota set for the `cpu` verb (that's the
   separate `cpu_limit` verb). The requested percentage is silently dropped end to end;
   measured load (~400%) has no relationship to the requested 90%.
3. **`kill` does not reproduce "RST" as specified**, independent of translate.go: on this
   platform, an already-open TCP connection to a `docker kill`-ed container observed a
   graceful close (EOF) rather than an exception indicating a reset, in every one of the
   repeated runs during development of this benchmark. The table's "client sees RST" is
   not what was measured here.
4. **`pause` and `down` are the two verbs that fully match the spec** as written, with no
   modifier for translate.go to mis-handle in either case.

**Publish:** a table, per platform (Linux/macOS/Docker Desktop). Fault fidelity varies by
platform and pretending otherwise is how people get bad data. Where a platform can't hit
tolerance, we say so rather than quietly widening the tolerance — see the MISS rows above,
published as measured, not smoothed over.

## B2 — Harness overhead

*What does routing through our proxy cost when no fault is active?*

Same scenario, three configurations: direct → through Toxiproxy → through Toxiproxy with
TortureU orchestrating. Report p50/p95/p99 deltas and max sustained rps.

**Publish:** the deltas, plus the generator's own ceiling on the test machine (fd limit,
ephemeral port range, CPU). A tool that reports "your backend maxes at 2k rps" when the
*generator* maxed out is worse than no tool — B2 exists so we never do that, and the
`achieved vs target` warning in `VERDICT.md` is its runtime counterpart.

## E1 — Attribution accuracy (the important one)

*Given a system with a known planted weakness, does TortureU find it and name the right cause?*

This is an **eval**, not a benchmark: a labelled corpus with ground truth.

### The corpus

A set of small backends, each with exactly one deliberate defect, in several languages
(Go / Python / Node / Java — because D-9 candidates are per-library and must be proven per-ecosystem):

| # | Planted defect | Correct verdict |
|---|---|---|
| 1 | HTTP client with no timeout | `caused` by dep latency; candidate = the client's timeout knob |
| 2 | Retry with no cap or backoff | retry storm under partial failure; candidate = retry config |
| 3 | Connection pool of 5 behind 500 rps | pool exhaustion; candidate = `MaxConns` |
| 4 | Non-idempotent consumer | duplicate side effects under redelivery |
| 5 | No circuit breaker on cascade path | one slow dep takes the service down |
| 6 | Unbounded in-memory queue | OOM under sustained spike |
| 7 | Cache stampede on expiry | thundering herd at TTL boundary |
| 8 | **Control: no defect** | `pass` — must not invent a finding |

Case 8 carries the most weight. A tool that always finds something is a random-number
generator with good typography.

### Metrics

```
detection      found a real defect                      / planted defects
attribution    named the CORRECT causing fault          / defects found
candidates     correct config knob in candidate list    / findings
false positive findings on the control case             (target: 0)
confidence     `caused` rate with OTel vs `correlated` without   (validates D-4)
```

**Publish:** per-case results, including failures. A corpus we score 100% on is a corpus we
overfit to — so cases are added when we *fail* in the wild, never trimmed when inconvenient.

## E2 — Detection accuracy

*Does `init` classify real repos correctly?*

Run `tortureu init` against N public repos with docker-compose files. Score:

- deps correctly typed / total deps
- external hosts found / external hosts present
- **gaps reported honestly** — an unclassified dep reported as a gap is a *success*, not a miss
  (D-3, R-DET-3)

E2 is the one place where "we didn't know" scores as a pass. Silently guessing scores as a
failure even when the guess is right, because a lucky guess and a wrong guess are the same
mechanism.

---

## Running them

B1 is real. B2 and the E1/E2 evals are still **planned, not built** — there is no CI job
gating on any of this yet:

```
make bench      # B1 fault fidelity — needs docker, ~2 min           (real, see benchmarks/b1)
make eval       # E1, E2 — needs docker, ~40 min                     (planned)
make bench-ci   # B2 + E1 subset, gates PRs on regression            (planned)
```

`make bench` builds and runs `benchmarks/b1` end to end: it brings up and tears down its own
short-lived docker-compose stacks (unique per-run names, force-remove backstops, cleanup
registered before any docker resource exists — mirroring `internal/run`'s own Docker-backed
tests), and always leaves `docker ps -a` exactly as it found it. Results land in
`benchmarks/results/<date>-<commit>.json` — both read from the environment at run time, never
hardcoded — and are tracked over time. `make eval` and `make bench-ci` are unchanged stubs
that print "not implemented" and exit non-zero; do not expect real numbers from them.

---

## Publishing rules

1. **Reproducible or unpublished.** Every number ships with the command and the machine spec.
2. **Failures included.** The E1 table shows cases we get wrong. A benchmark page with no
   losses is marketing, and developers read it as such.
3. **No competitor benchmarks.** We do not publish "TortureU vs k6" numbers. We *drive* k6 —
   benchmarking our own dependency would be both dishonest and stupid.
4. **Third-party findings need consent.** If E2 or any corpus run surfaces a real weakness in
   someone else's public repo, that goes to them privately first, and is published only with
   their agreement or not at all. Non-negotiable: the fastest way to burn trust in a security-
   adjacent tool is to publish someone's outage as a marketing asset.
5. **Platform-specific results stay labelled.** No averaging across Linux and Docker Desktop
   to make a number look better.

---

## Status

**B1 is measured** (Linux/Docker Engine, single platform, see the table above and
`benchmarks/results/2026-08-08-07acb03.json`): 4 of 7 `inject:` verbs miss their tolerance
as currently shipped, driven through the real fault path with no shortcuts. `down` and
`pause` pass as written; `latency`, `jitter`, and `bandwidth` fail because
`internal/fault/translate.go` never converts the unit-suffixed strings a human-authored
`torture.yaml` actually produces (`"300ms"`, `"1mbps"`) into the numeric values Toxiproxy's
API requires — confirmed by running the real translate → apply path against a real
Toxiproxy and reading its rejection back, not inferred from reading the source; `cpu` fails
because `translateDocker`'s `Args["amount"]` is silently never read by
`DockerApplier.ApplyDocker`; and `kill` fails because the observed connection behavior
(graceful close) does not match the spec's "client sees RST." This is a real, load-bearing
limitation of the shipped instrument today, not something fixed as part of building this
benchmark — B1's own constraints kept it out of `internal/**`, and fixing translate.go is
future work tracked by this finding, not by this file's Status changing to something rosier.

**B2 (harness overhead), E1 (attribution accuracy), and E2 (detection accuracy) are still
not measured.** No `make eval`/`make bench-ci` exists yet beyond the "not implemented"
stubs in the Makefile. Until B2 exists, B1's numbers say nothing about whether the harness
itself perturbs what it measures; until E1 exists, TortureU's actual product claim (finding
the *right* cause) remains unverified by anything in this repository.
