# Verdict Schema & Agent Surface

The normalized output every run produces, and the MCP tools that return it.

**Why this file is the product:** k6 reports p99, Toxiproxy caused the fault, the logs hold the
retries — and nothing joins them. That join, expressed once in a shape both humans and agents read,
is the thing no single vendor can ship because it spans their competitors (see RESEARCH.md §17).
Everything else in TortureU is plumbing that feeds this document.

Constraints this inherits: **D-4** (confidence levels), **D-7** (phase anchors), **DC-1** (noun
rule), **DC-2** (egress audit).

---

## 1. The verdict object

One JSON document per run. Stable field names — agents parse this.

```jsonc
{
  "run_id": "2026-08-08T14:22:01Z-checkout-spike-a3f",
  "scenario": "checkout-spike",
  "status": "fail",                    // pass | fail | error | aborted
  "started_at": "2026-08-08T14:22:01Z",
  "duration_s": 280,
  "commit": "a3f19c2",                 // for §12 trend tracking
  "reset": "clean",                    // clean | skipped  (D-8)

  // ── Why it failed. Empty array when status=pass. Ordered worst-first.
  "findings": [
    {
      "id": "f1",
      "confidence": "caused",          // caused | correlated | ambiguous  (D-4)

      "broke": {
        "assertion": "http_req_duration: p(99)<1500",
        "observed": "4218ms",
        "at": "peak+12s",
        "sustained_s": 47
      },

      "cause": {
        "fault": "pg_slow",
        "target": "postgres:5432",
        "inject": { "latency": "300ms", "jitter": "50ms" },
        "window": ["peak", "peak+60s"]
      },

      "chain": [                       // fault -> symptom, one hop per line
        { "at": "postgres:5432",  "observed": "query latency 4ms -> 304ms" },
        { "at": "pgx pool",       "observed": "acquire wait 0ms -> 3.9s, queue depth 47/20" },
        { "at": "checkout-api",   "observed": "p99 210ms -> 4218ms" },
        { "at": "client",         "observed": "retry rate 0.1/s -> 84/s" }
      ],

      // D-9: candidate config surface, NOT a file:line. The agent does the last mile.
      "candidates": [
        { "library": "jackc/pgx", "source": "go.mod",
          "knobs": ["MaxConns", "MinConns", "ConnConfig.ConnectTimeout"] },
        { "library": "cenkalti/backoff", "source": "go.mod",
          "knobs": ["MaxRetries", "InitialInterval", "MaxElapsedTime"] }
      ],

      "amplification": "20-conn pool + 3x retry = 300ms dep latency became 4.2s user latency"
    }
  ],

  // ── What held. Presence matters: proves the run exercised these, not that they were skipped.
  "passed": [
    { "assertion": "http_req_failed: rate<0.01", "observed": "0.003" },
    { "assertion": "promql: orders_total == payments_total", "observed": "true" }
  ],

  // ── DC-2 proof. Auditable evidence nothing escaped.
  "egress_audit": {
    "mocked":       ["api.stripe.com", "api.twilio.com"],
    "blocked":      ["telemetry.vendor.io"],
    "real":         [],
    "unclassified": []                 // non-empty => status=aborted, run never started
  },

  // ── D-4: what this repo's observability can actually support.
  "observability": {
    "traces": true, "metrics": true, "logs": true,
    "max_confidence": "caused"
  },

  "metrics": {
    "rps":  { "target": 5000, "achieved": 4870 },
    "http_req_duration": { "p50": 180, "p95": 890, "p99": 4218 },
    "http_req_failed": 0.003
  },

  "artifacts": {
    "k6_summary": "runs/a3f/k6.json",
    "traces":     "runs/a3f/traces.otlp",
    "flamegraph": "runs/a3f/cpu.svg"
  }
}
```

### Field rules

- **`status: error` vs `fail`** — `fail` means the system under test broke an assertion (a *result*).
  `error` means TortureU broke (adapter crash, compose won't start). Never conflate: a harness bug
  reported as a SUT failure sends the agent hunting a bug that isn't there.
- **`status: aborted`** — DC-2 refused to start (unclassified egress) or reset failed. No findings,
  no metrics, non-zero exit.
- **`confidence` is per-finding, not per-run.** One run can prove one thing and merely correlate
  another.
- **`achieved` vs `target` rps** — if `achieved` trails `target` by >5%, the *load generator* may be
  the bottleneck, not the SUT. Flagged as a warning, because a large share of "our backend maxes at
  2k rps" results are actually the machine running k6 (fd limits, ephemeral ports, generator CPU).
- **`passed` is not decoration.** Without it, an agent can't distinguish "the integrity check held"
  from "the integrity check never ran."

---

## 2. Exit codes

```
0   pass
1   fail            — SUT broke an assertion
2   error           — TortureU or an adapter failed
3   aborted         — DC-2 unclassified egress, or reset failed
4   inconclusive    — ran clean, but all findings are `ambiguous` (D-4)
```

`4` exists so CI doesn't merge on a green that means "we couldn't tell." Treating ambiguous as pass
is how a harness quietly stops finding anything.

---

## 3. MCP tools (DC-1 noun rule)

Nouns: `experiment`, `fault`, `slo`, `verdict`. Never `script`, `test`, `threshold` — those are k6's.

### `describe_system()`
Detection output (D-3), including known-unknowns. No run required.
```jsonc
{
  "services": [{ "name": "checkout-api", "image": "...", "ports": [8080], "language": "go" }],
  "deps": [{ "name": "postgres", "type": "postgresql", "client": "jackc/pgx", "from": "go.mod" }],
  "egress": { "classified": 3, "unclassified": ["api.partner.com"] },
  "observability": { "traces": false, "max_confidence": "correlated" },
  "gaps": ["api.partner.com reached from source we did not parse — classify it in torture.yaml"]
}
```
`gaps` is deliberate: D-3 caps detection at compose+lockfile, so unknowns are **reported, never
guessed**. This is the agent's cue to read the source itself.

### `propose_experiments()`
Ranked scenarios for the detected topology. Returns `torture.yaml` fragments, not prose.
Ranking = likely blast radius × cheapness to run. A Postgres latency fault against a service with
one connection pool ranks above a CPU squeeze, because pool exhaustion amplifies and CPU doesn't.

### `run_experiment(name, {no_reset?})`
Returns the §1 verdict object. This is the only tool that executes anything.

### `explain_failure(run_id, finding_id)`
The §1 `finding` expanded: full trace spans in the window, log excerpts, and the `candidates` list.
Deliberately stops at *candidate config surface* — D-9.

### `emit_k6_script(name)`
Escape hatch. Returns the compiled k6 script for a scenario `torture.yaml` can't express. After
this, k6's own skills and MCP are the right tool — that handoff is the point of DC-1, not a
workaround for it.

---

## 4. Human output

Same data, rendered. No second code path — the CLI formats the JSON.

```
FAIL  checkout-spike                                    280s  commit a3f19c2

  ✗ http_req_duration p(99)<1500 -> 4218ms   at peak+12s, sustained 47s
    caused by  pg_slow (postgres:5432, +300ms)              [confidence: caused]

    postgres      query latency   4ms -> 304ms
    pgx pool      acquire wait    0ms -> 3.9s   queue 47/20
    checkout-api  p99           210ms -> 4218ms
    client        retry rate    0.1/s -> 84/s

    20-conn pool + 3x retry turned 300ms of dep latency into 4.2s of user latency.

    look at:  jackc/pgx        MaxConns, ConnConfig.ConnectTimeout
              cenkalti/backoff MaxRetries, InitialInterval

  ✓ http_req_failed rate<0.01          0.003
  ✓ orders_total == payments_total     true

  egress: 2 mocked, 1 blocked, 0 real          exit 1
```

The amplification line is the sentence a human takes to standup, and the `look at:` block is the
same list the agent gets. One artifact, two audiences.

---

## Open

- **Storage format for trend tracking.** Verdicts are self-describing JSON; whether that's SQLite,
  JSONL, or Bencher-compatible depends on whether comparison is local-only or shared. Deferred until
  there are runs worth comparing.
