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
| `jitter: 50ms` | uniform ±50ms → stddev ≈ 28.9ms (`j/√3`) | stddev of delta | ±15% |
| `bandwidth: 1mbps` | 1 Mbps | bytes/sec through the proxy | ±5% |
| `down` | connection refused | error class at client | exact |
| `pause` (SIGSTOP) | no response, conn held open | client sees timeout not RST | exact |
| `kill` (SIGKILL) | SIGKILL delivered, exit 137 | signal + exit code (**not** client RST — see R-EXE-25) | exact |
| `cpu: 90%` | 90% of quota | cgroup cpu.stat | ±5% |

**Platform:** Linux 7.0.0-29-generic, Docker 29.5.3, AMD Ryzen 7 5800H (16 cores), cgroup v2.
Measured `2026-08-09T08:02:43Z` at commit `72ca62b`
([full JSON](benchmarks/results/2026-08-08-698d549.json)). Not yet measured on macOS or
Docker Desktop — those rows do not exist yet, and BENCHMARKS.md does not claim they do.

This is the third measured run, after three upstream fixes landed since the second run
(commit `bb6723c`, [JSON](benchmarks/results/2026-08-08-bb6723c.json)):

- **`cpu` fixed** (`e297ad6`): `--cpu-load` was applied undivided per worker, so
  `workers: 4` at `cpu: 90%` summed to ~360% instead of the requested total. Now divided by
  worker count before being passed to `stress-ng`.
- **`jitter`'s tolerance corrected** (`ccbcf07`, R-EXE-24): Toxiproxy's `latency` toxic adds
  a *uniform* offset in `[-jitter, +jitter]`, so the achievable stddev is `jitter/√3` ≈
  28.9ms for `jitter: 50ms`, not 50ms itself. The tolerance table and this harness's own
  verdict computation are both updated to check against that derived target.
- **`kill`'s expectation corrected** (`18ef2a8`, R-EXE-25): a client-visible RST is not
  achievable via `docker kill` — an idle, drained TCP socket gets an orderly FIN on process
  death regardless of which signal killed it, and `DockerApplier` cannot reach the target's
  own socket options to force an abortive close. `kill`/`graceful` are distinct at the
  **signal and exit-code** layer (SIGKILL/137 vs SIGTERM/0) instead; this harness now reads
  the echo container's own `docker inspect` state (before Teardown's `docker start` undo
  runs and erases it) rather than watching the client's TCP connection.

**Results** (unit-suffixed strings exactly as a human-authored `torture.yaml` writes them,
e.g. `inject: { latency: 300ms, jitter: 50ms }`, run through the real `fault.Translate` →
`fault.Manager` → `ToxiproxyApplier`/`DockerApplier` path):

| Fault | Requested | Measured | Tolerance | Verdict |
|---|---|---|---|---|
| `latency: 300ms` | +300ms | p50 delta = 301.83ms (n=40, jitter-free toxic) | ±10ms | **PASS** (0.6% off) |
| `jitter: 50ms` | stddev target 28.87ms (`50/√3`) | stddev of delta = 26.33ms (n=40, combined toxic) | ±15% | **PASS** (8.8% off target) |
| `bandwidth: 1mbps` | 1 Mbps (125,000 B/s) | 123,206 bytes/sec through the proxy (1.4% off) | ±5% | **PASS** |
| `down` | connection refused | client saw `ConnectionRefusedError` | exact | **PASS** |
| `pause` (SIGSTOP-equivalent freeze) | no response, conn held open | 5/5 post-fault attempts timed out on an open connection, no RST | exact | **PASS** |
| `kill` (SIGKILL) | signal + exit code: SIGKILL, exit 137 | `docker inspect` on the echo container read `status=exited exit_code=137` | exact | **PASS** |
| `cpu: 90%` | 90% of quota | cgroup v2 `cpu.stat` measured 94.1% of one core (`--cpu-load` now divided across 4 workers) | ±5% | **PASS** |

**All seven rows pass.** `jitter` and `kill` — both MISSes in an earlier run — pass against the
corrected tolerance and the corrected measurement layer respectively; `cpu` passes against the fixed
divide-by-workers logic; and `latency` passes now that it is measured from a jitter-free toxic
instead of from a signal this benchmark deliberately adds ±50ms of noise to. See finding 1 for why
that was a measurement defect rather than the fidelity regression it was published as.

**Findings:**

1. **The `latency` MISS was this benchmark's own defect, not a fidelity problem — and the first
   explanation given for it was wrong.** It was published as a miss twice (289.85ms, then
   288.82ms) and both times attributed to contention from other workloads on this machine. That
   was a guess that happened to point away from the real cause.

   The row was measured from the *jittered* toxic (`latency: 300ms` with `jitter: 50ms`), so the
   samples are uniform over 250–350ms and the **median** of n=40 has a standard error of
   `jitter/√n` = **7.9ms**. A ±10ms tolerance is 1.26 standard errors, so a perfectly correct
   implementation is reported as a MISS **20.6% of the time**. Both published misses were within
   1.5 SE of 300 — indistinguishable from correct. The earlier passes at 297.56ms and 295.34ms
   were the same distribution landing on the other side, which is why "it passed before" felt like
   evidence and was not.

   Fixed by isolating the variable: `latency` is now measured from a **jitter-free** toxic, so
   ±10ms is compared against a signal whose only spread is real network noise. Measured twice at
   301.82ms and 301.83ms — 0.6% off. `jitter` still comes from the combined toxic, because
   measuring jitter needs jitter.

   The lesson is worth more than the row: a tolerance narrower than the measurement's own sampling
   error does not test the tool, it tests luck. This one produced a false negative that stood in
   this file — and in the launch go/no-go — for two days.
2. **`jitter` passes against the corrected, uniform-distribution tolerance (R-EXE-24).**
   Measured stddev 26.33ms vs. the derived target 28.87ms (`jitter/√3`) is 8.8% off, inside
   ±15%. Across runs this row has measured 27.95ms, 30.74ms and 26.33ms — a spread of ~4ms,
   which is the sampling noise finding 1 quantifies, not drift in fault delivery. The first of
   those was once read as a 44% miss against the *old*, mistaken σ=jitter assumption; nothing
   about the fault delivery changed then either, only the tolerance's stated expectation.
3. **`kill` now passes, measured at the correct layer (R-EXE-25).** The fault was always
   being delivered (SIGKILL was always sent); the earlier MISS was this benchmark asking a
   question — "does the client see an RST?" — that `docker kill` was never going to be able
   to answer yes to, on this or any Linux kernel, for a process whose sockets have no queued
   unread data or explicit abortive-close option at the moment of death. Measuring the
   signal/exit-code layer directly (`docker inspect`'s `.State.Status`/`.State.ExitCode`)
   answers the question `kill` actually specifies.
4. **`cpu: 90%` now passes with the divide-by-workers fix.** 94.1% of one core against a
   90% target is 4.6% off, inside ±5%. This is an honest integer-worker approximation (4
   workers each targeting `90/4 = 22.5%` rounds to `23%`, summing to `92%` in principle;
   94.1% measured here is within normal `stress-ng`/cgroup-accounting noise of that).
5. **`down` and `pause` continue to fully match the spec**, unchanged from every prior run.

**Correction, 2026-08-09: the `latency` MISS was a broken measurement, not a fidelity defect.**
It was published twice as a miss (289.85ms, then 288.82ms) and twice explained away as machine
contention. It was neither. The row was measured from the *jittered* toxic — `latency: 300ms` with
`jitter: 50ms` — so the samples are uniform over 250–350ms and the **median** of n=40 of them has a
standard error of `jitter/√n` = **7.9ms**. A ±10ms tolerance is therefore only 1.26 standard errors,
and a perfectly correct implementation reads as a MISS **20.6% of the time**. Both published misses
sat within 1.5 SE of 300 — statistically indistinguishable from correct.

The tolerance was not too tight for Toxiproxy; it was tighter than the noise this benchmark adds on
purpose. The latency row is now measured from a **jitter-free** toxic, so ±10ms is compared against
a signal whose only spread is real network noise. Measured twice at 301.82ms and 301.83ms. The
jitter row still comes from the combined toxic, because measuring jitter needs jitter.

This is worth stating plainly: a benchmark whose tolerance is narrower than its own sampling error
does not measure the tool, it measures luck — and it produced a false negative that stood in this
file, and in the go/no-go analysis, for two days.

**Publish:** a table, per platform (Linux/macOS/Docker Desktop). Fault fidelity varies by
platform and pretending otherwise is how people get bad data. Where a platform can't hit
tolerance, we say so rather than quietly widening the tolerance — see the `latency` MISS
above, published as measured, not smoothed over.

## B2 — Harness overhead

*What does routing through our proxy cost when no fault is active?*

This is now real: `benchmarks/b2` drives the same scenario — 8 concurrent persistent TCP
connections to the known-good echo service, round-tripping a 64-byte payload as fast as
possible for a 5-second sustained window — through three configurations, all via the same
`ComposeTopologyApplier`/`ToxiproxyApplier`/`fault.Manager` production path B1 uses:

1. **direct** — client dials the echo service with no proxy on the connection path at all.
2. **toxiproxy** — client dials the real Toxiproxy proxy, with no toxic installed.
3. **tortureu** — same proxy path, but with a real, zero-effect toxic (`latency: 0, jitter:
   0`) applied through the actual production `fault.Translate` → `fault.Manager` →
   `ToxiproxyApplier` path, isolating "TortureU's orchestration layer is active" from "a
   fault is distorting traffic" (a separate, already-measured question — B1).

`make bench` runs B1 then B2; results land in
`benchmarks/results/<date>-<commit>-b2.json`.

**Platform:** Linux 7.0.0-29-generic, Docker 29.5.3, AMD Ryzen 7 5800H (16 cores). Measured
twice: at commit `698d549` ([full JSON](benchmarks/results/2026-08-08-698d549-b2.json)) and
re-run at `74c4517` ([full JSON](benchmarks/results/2026-08-08-74c4517-b2.json)) after the
orchestrator gained co-driven load (`--db-load`, `--fuzz`) and its marker channel was tee'd —
changes on the run path, so the overhead claim was re-measured rather than assumed to hold.

**Results** (p50/p95/p99 in milliseconds, deltas against the `direct` baseline; rps is
requests actually completed divided by the *real* wall-clock window the load ran in, not the
requested duration):

| Config | p50 | p95 | p99 | rps | Δp50 | Δp95 | Δp99 | Δrps |
|---|---|---|---|---|---|---|---|---|
| direct | 0.59ms | 2.74ms | 5.82ms | 8,610.6 | — | — | — | — |
| toxiproxy (no toxic) | 0.68ms | 2.78ms | 5.46ms | 7,961.3 | +0.08ms | +0.04ms | −0.36ms | −7.5% |
| tortureu (orchestrated, zero-effect toxic) | 0.69ms | 2.92ms | 5.69ms | 7,758.8 | +0.09ms | +0.18ms | −0.13ms | −9.9% |

Re-run at `74c4517`, same machine, same scenario:

| Config | p50 | p95 | p99 | rps | Δp50 | Δp95 | Δp99 | Δrps |
|---|---|---|---|---|---|---|---|---|
| direct | 0.47ms | 1.36ms | 1.99ms | 13,662.4 | — | — | — | — |
| toxiproxy (no toxic) | 0.47ms | 1.30ms | 1.88ms | 13,829.8 | −0.00ms | −0.06ms | −0.11ms | +1.2% |
| tortureu (orchestrated, zero-effect toxic) | 0.47ms | 1.26ms | 1.82ms | 14,123.9 | −0.01ms | −0.10ms | −0.17ms | +3.4% |

**Read these two runs together, not separately.** The first measured the orchestrated path
9.9% *slower* than direct; the second measured it 3.4% *faster*, and the whole absolute
scale moved (direct p99 5.82ms → 1.99ms) on an unchanged machine and scenario. A proxy hop
cannot make traffic faster, so the honest reading is that **the orchestration overhead is
smaller than this harness's run-to-run variance** — most likely host contention, since these
runs shared a machine with concurrent Docker workloads. The supportable claim is therefore
"no overhead measurable at this scale", not a signed percentage in either direction. Pinning
it down would need repeated trials on a quiet machine with a confidence interval, which has
not been done.

**Generator ceiling** (BENCHMARKS.md's own rule: a tool that reports "your backend maxes at
2k rps" when the *generator* maxed out is worse than no tool), read from inside the actual
load-generating client container:

- fd limit (`ulimit -n`): **1,048,576** — not the bottleneck at 8 concurrent connections.
- ephemeral port range (`/proc/sys/net/ipv4/ip_local_port_range`): **32768–60999** — not
  exercised meaningfully by this scenario, since each of the 8 workers holds one persistent
  connection open for the whole window rather than opening a new one per request.
- CPU: AMD Ryzen 7 5800H, 16 cores — the harness uses a single Python process with 8
  threads inside one container; the ~8–9k rps ceiling measured here is very likely this
  generator's own single-process-Python throughput limit (GIL-bound thread scheduling), not
  a limit the echo service or proxy path imposed. This benchmark is measuring *relative
  overhead* (the deltas), which does not depend on the absolute ceiling being the true
  system limit, but the absolute rps numbers above should not be read as "TortureU's proxy
  supports ~8k rps" — that would be exactly the "generator maxed out, not the backend"
  mistake this rule exists to prevent.

**Findings:**

1. Toxiproxy alone adds a small, consistent tax: +0.08ms p50, roughly flat p95, and a
   7.5% rps drop against the direct baseline — expected for adding one extra network hop
   with its own userspace read/write loop.
2. TortureU's own orchestration layer (a live, zero-effect toxic applied through the full
   production path) adds no further meaningful latency beyond bare Toxiproxy (+0.01ms p50
   versus the toxiproxy row) — consistent with the toxic being applied once, at fault-apply
   time, over Toxiproxy's control API, rather than adding any per-request work of its own.
   The extra ~2.4% rps drop between `toxiproxy` and `tortureu` is within the run-to-run noise
   this single 5-second, single-process-generator measurement produces (see the generator
   ceiling note above) and is not attributed to a specific mechanism.
3. p99 in both proxied configurations is not reliably worse than direct (both came in
   *lower* than direct's p99 in this run) — at this sample size and duration, tail latency is
   dominated by generator/OS scheduling noise, not the proxy path. A longer sustained window
   and multiple repeated runs would be needed before treating any p99 delta here as
   meaningful; this run reports what was measured, not a claim that the proxy improves tail
   latency.

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
| 9 | Two simultaneous faults, one hot dependency (OTel-instrumented) | `caused` by the fault whose target actually degraded, from real spans |

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

### Results

Measured `2026-08-09T00:16:51Z`, whole corpus in one run
(`bash evals/run_case.sh`), every case driven through the real `tortureu run` binary. Per-case
verdict JSON is in [`evals/results/`](evals/results/).

| # | Planted defect | status | findings | attributed cause | confidence | candidate knob |
|---|---|---|---|---|---|---|
| 1 | HTTP client with no timeout | fail | 1 | `dep_slow` | correlated | ✅ `Client.Timeout` |
| 2 | Retry with no cap or backoff | fail | 1 | `dep_down` | correlated | ❌ none exists — see below |
| 3 | Connection pool of 5 behind 500 rps | fail | 2 | *ambiguous* | ambiguous | ✅ `MaxConns` |
| 4 | Non-idempotent consumer | fail | 1 | `order_duplicate` | correlated | n/a |
| 5 | No circuit breaker on cascade path | fail | 1 | `dep_slow` | correlated | n/a |
| 6 | Unbounded in-memory queue | fail | 1 | *ambiguous* | ambiguous | n/a |
| 7 | Cache stampede on expiry | fail | 1 | *ambiguous* | ambiguous | n/a |
| 8 | **Control: no defect** | **pass** | **0** | — | — | — |
| 9 | Two faults at once, one hot dependency (OTel) | fail | 1 | `dep_a_slow` | **caused** | ✅ `Client.Timeout` |

```
detection      8/8   every planted defect produced a finding
attribution    5/8   findings that named a causing fault
candidates     3/4   cases whose ground truth names a config knob
false positive 0     on the control (the metric that carries the most weight)
confidence     1/1   the one OTel-instrumented case reaches `caused`; the seven without
                     tracing top out at `correlated`, which is exactly D-4's claim
```

**Where this is weaker than it looks, stated rather than buried:**

- **Attribution is 5/8, not 8/8 — and the earlier reading of *why* was wrong.** This document
  previously said cases 3, 6 and 7 return `ambiguous` because "more than one fault was active".
  They declare **no faults at all** (check their `torture.yaml`: case 3 is a pool sized 5, case 6
  an unbounded queue, case 7 a cache stampede — all three are static defects under load, with
  nothing injected). With zero faults there is no candidate cause to name, so no attribution
  mechanism, traces included, may name one: `ambiguous` is the only honest label and those three
  are a permanent floor on this metric, not a defect to fix. Multi-fault attribution — the thing
  the old note described — is now implemented (**R-VER-17**) and is what case 9 measures: two
  faults active, and the spans say which target actually degraded. On the denominator that rule
  can affect, findings from runs that *had* faults, attribution is 5/5.
- **5/8 is below the 70% bar this project set for itself (62.5%).** Stated rather than smoothed
  over: the bar is not met on the whole-corpus denominator, and it cannot be met by improving
  attribution, because three of the eight cases have nothing to attribute to. Either the metric's
  denominator is wrong (it counts findings whose ground truth has no causing fault) or the corpus
  needs more fault-driven cases. Both are corpus/metric work, not verdict-layer work; neither is
  done here, and the corpus was deliberately not tuned to make the number look better.
- **Case 2's candidate miss is structural, not a bug.** Its ground truth is "retry config", but
  the planted defect is hand-rolled retry over Go's `net/http`, which has **no** retry knob to
  name. No candidate list can supply one; the honest fix is code, not configuration. Counting it
  as a miss keeps the score honest, but the ceiling here is 2/3, not 3/3.
- **`caused` is now reached, by exactly one case.** Case 9 is the corpus's first
  OpenTelemetry-instrumented fixture: a Go SUT exporting real OTLP spans to a real Jaeger in its
  own compose stack, two faults firing at once (`dep-a` slowed 3s, `dep-b` taken down), and only
  `dep-a` on the un-timed hot path. The verdict names `dep_a_slow` and carries the chain it read
  that from (`dep-a:9091  latency 845µs -> 3002.8ms (n=200 spans)`). The other seven cases still
  top out at `correlated` because they run no tracing at all — which is D-4's claim, now measured
  on both sides of it rather than only one. One instrumented case is not a validated `confidence`
  metric; it is one data point.
- **Case 9 found two real defects in the code it exercises, both fixed here rather than worked
  around.** (a) Trace ingestion used a plain host-process HTTP client, so a Jaeger inside the
  DC-2-isolated stack — which has no published host port — was unreachable, and *no* real run
  could ever have built a chain. It now uses the same reach-into-the-stack transport as the
  Prometheus and broker clients (**R-VER-13**). (b) The degradation gate was a bare 2x ratio, and
  at sub-millisecond span durations jitter clears it routinely: case 9's *undisturbed* dependency
  measured 587µs -> 1.3ms (2.2x) next to the faulted one's 817µs -> 3002ms, which made one real
  cause and one noisy neighbour look like two candidates and refused to name either. The gate now
  also requires a 10ms absolute step (**R-VER-13**).
- **8 cases plus 1 control is a small corpus.** It is enough to catch a tool that invents
  findings; it is not enough to put a confidence interval on 5/8.

**A note on the harness itself.** The launch gate used to check only that the control produced zero
findings. An aborted run also produces zero findings, so a corpus that failed to start printed
`OK: case 8 produced 0 findings` and exited 0 — the same false green E1 exists to catch, inside the
thing that catches it. It now requires the control to be `status=pass` and every case to have
produced a readable verdict, and refuses to score a corpus where any case aborted.

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

B1 and B2 are both real now. `make bench-ci` is still **planned, not built** — there is no
CI job gating on a regression yet:

```
make bench      # B1 fault fidelity + B2 harness overhead — needs docker, ~3 min   (real, see benchmarks/b1, benchmarks/b2)
make eval       # E1 — needs docker                                                (real, see evals/run_case.sh)
make bench-ci   # B2 + E1 subset, gates PRs on regression                          (planned)
```

`make bench` builds and runs `benchmarks/b1` then `benchmarks/b2` end to end: each brings up
and tears down its own short-lived docker-compose stacks (unique per-run names, force-remove
backstops, cleanup registered before any docker resource exists — mirroring `internal/run`'s
own Docker-backed tests), and always leaves `docker ps -a` exactly as it found it. Results
land in `benchmarks/results/<date>-<commit>.json` (B1) and
`benchmarks/results/<date>-<commit>-b2.json` (B2) — date and commit both read from the
environment at run time, never hardcoded — and are tracked over time. `make bench-ci` is an
unchanged stub that prints "not implemented" and exits non-zero; do not expect real numbers
from it.

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
`benchmarks/results/2026-08-09-e5402e3.json`): **all 7** `inject:` verbs pass tolerance as of
the latest measured run, driven through the real fault path with no shortcuts. The first run
(commit `07acb03`) found `internal/fault/translate.go` never converted the unit-suffixed
strings a human-authored `torture.yaml` actually produces into the numeric values Toxiproxy's
API requires; the second run (`bb6723c`) reverified that fix and surfaced three further
findings — `cpu` silently dropped its requested load percentage, `jitter`'s tolerance assumed
the wrong distribution, and `kill`'s spec asked for something (`docker kill` producing a
client-visible RST) that is not achievable on Linux. All three were then corrected upstream
(`e297ad6` fixed `cpu`'s divide-by-workers bug; `ccbcf07`/R-EXE-24 corrected `jitter`'s
tolerance to the uniform-distribution target; `18ef2a8`/R-EXE-25 corrected `kill`'s spec to
the signal/exit-code layer and this harness now measures that layer directly). This third run
reverifies all three: `cpu`, `jitter`, and `kill` all pass now. The one remaining miss,
`latency` (missed by 0.15ms against a ±10ms budget), is reported as measurement noise on a
loaded shared machine rather than a fidelity regression — see the finding above — but is
published as a MISS, not rounded away, per this file's own rule. B1's own constraints kept
this benchmark out of `internal/**` throughout all three runs — every defect found was
reported, and every fix was made and verified by `internal/run`'s owner, not this benchmark.

**B2 is measured** (same platform, see the table above and
`benchmarks/results/2026-08-08-698d549-b2.json`): Toxiproxy alone costs a small, consistent
tax (+0.08ms p50, −7.5% rps against a direct baseline); TortureU's own orchestration layer
adds no further meaningful latency on top of bare Toxiproxy. The generator's own ceiling
(fd limit, ephemeral port range, CPU) is published alongside the rps numbers specifically so
the ~8k rps figures are not misread as "this is what TortureU's proxy path supports" — at
this scenario's concurrency, the single-process Python load generator is the more likely
ceiling, not the proxy.

**E1 (attribution accuracy) is measured** via `make eval` (`evals/run_case.sh`); its per-case
results, metrics and limitations are in this file's E1 Results table above. **E2 (detection accuracy) is still not measured** —
no harness exists yet for it, and `make bench-ci` remains an unimplemented stub.
