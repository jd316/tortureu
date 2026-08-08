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
requested and observed effect.

| Fault | Requested | Measured | Tolerance |
|---|---|---|---|
| `latency: 300ms` | +300ms | p50 delta at the client | ±10ms |
| `jitter: 50ms` | σ=50ms | stddev of delta | ±15% |
| `bandwidth: 1mbps` | 1 Mbps | bytes/sec through the proxy | ±5% |
| `down` | connection refused | error class at client | exact |
| `pause` (SIGSTOP) | no response, conn held open | client sees timeout not RST | exact |
| `kill` (SIGKILL) | conn reset | client sees RST | exact |
| `cpu: 90%` | 90% of quota | cgroup cpu.stat | ±5% |

**Publish:** a table, per platform (Linux/macOS/Docker Desktop). Fault fidelity varies by
platform and pretending otherwise is how people get bad data. Where a platform can't hit
tolerance, we say so rather than quietly widening the tolerance.

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

## Running them — **planned, not built**

None of this exists yet. There is no `Makefile`, no `benchmarks/` directory, and no CI job that
gates on a benchmark. It is written down as the intended shape, not as instructions you can follow:

```
make bench      # B1, B2 — needs docker, ~10 min          (planned)
make eval       # E1, E2 — needs docker, ~40 min          (planned)
make bench-ci   # B2 + E1 subset, gates PRs on regression (planned)
```

Results would land in `benchmarks/results/<date>-<commit>.json` and be tracked over time. Until
that exists, no number in this file has been measured — see Status at the bottom, which has said so
from the start.

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

Nothing is measured yet. B1 is the first to build — it validates the core `inject:` verbs, and
until it passes, every other number in this file rests on an unverified instrument.
