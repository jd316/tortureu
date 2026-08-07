# TortureU

**Load testing tells you it got slow. TortureU tells you which dependency did it.**

One CLI that drives load and fault injection **on the same clock**, against your local
`docker-compose` stack, and returns a single verdict naming what broke and why.

> **Status: pre-alpha.** The design is complete and specified; the implementation has just started.
> Nothing here works yet. Watch the repo rather than installing it.

## The problem

Your load test ramps to 5k rps and passes. Then production degrades because Postgres got 300 ms
slower, a 20-connection pool drained, retries with no backoff tripled the load, and p99 went to 4
seconds. No load generator shows you that, because none of them can make Postgres slow *while*
generating load.

Chaos tools can inject the fault — but they need Kubernetes. Nothing lets you say this on a laptop:

```yaml
load:
  stages:
    - { phase: peak, hold: 500rps, for: 180s }
faults:
  - { at: peak, target: postgres:5432, inject: { latency: 300ms } }
assert:
  - http_req_duration: ["p(99)<1500"]
```

## What comes back

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
```

It names a *candidate config surface*, not a `file:line` — finding the exact constant is the job of
whoever (or whatever) reads this. `tortureu mcp` hands the same structure to a coding agent.

## Design

| Document | What it is |
|---|---|
| [`SPEC.md`](SPEC.md) | normative. 108 numbered requirements. Build against this. |
| [`RESEARCH.md`](RESEARCH.md) | the survey: 151 tools across 19 domains, and why each is driven, delegated or merely named |
| [`VERDICT.md`](VERDICT.md) | verdict schema, exit codes, MCP surface |
| [`BENCHMARKS.md`](BENCHMARKS.md) | how this gets evaluated, and what we refuse to claim |
| [`registry.yaml`](registry.yaml) | the tool catalog |

Two constraints are load-bearing and worth reading before contributing:

- **Egress denies by default.** An unclassified external host aborts the run. A 100× replay against
  someone's real API is an outage you cause them.
- **We don't compete with k6's vocabulary.** TortureU's nouns are `experiment`, `fault`, `slo`,
  `verdict`. k6 owns `script`, `test`, `threshold`. Both MCPs coexist.

## Development

```bash
go test ./...      # unit tests
python3 check.py   # spec/docs/registry consistency + requirement traceability
```

Spec-driven and test-driven: state the requirement in `SPEC.md` first, write a test citing its id,
watch it fail, then implement. `check.py` fails on a test citing a requirement that doesn't exist.

## Licence

MIT. TortureU invokes k6 (AGPL-3.0) as a separate, unmodified process and never links against it —
see `SPEC.md` §10.
