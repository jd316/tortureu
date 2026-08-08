# TortureU

**Load testing tells you it got slow. TortureU tells you which dependency did it.**

One CLI that drives load and fault injection **on the same clock**, against your local
`docker-compose` stack, and returns a single verdict naming what broke and why.

> **Status: alpha.** The core works and is proven against real Docker: load and faults on one
> clock, topological egress isolation, fault interception on internal dependencies, and a verdict
> that names the causing fault. `init`, `run` and `doctor` are real verbs.
>
> **Not yet:** `smoke`, `check`, `emit`, `capture` and `replay` exit 2 (`not implemented in v0`),
> and `mcp` lists its tool surface without speaking a transport — the five MCP tools exist as a
> library, not yet as a server. Trace ingestion is absent, so verdicts report `correlated`
> attribution rather than `caused`. See [`SPEC.md` §12](SPEC.md) for what is deliberately open.

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
FAIL  checkout-spike                                              280s

  ✗ http_req_duration p(95)<500 -> 4218ms
    caused by  pg_slow (postgres:5432, latency)         [confidence: correlated]

    look at:  jackc/pgx   MaxConns, MinConns, ConnConfig.ConnectTimeout

  ✓ http_req_failed rate<0.01          0.003

  egress: 2 internal, 0 unclassified                              exit 1
```

It names a *candidate config surface*, not a `file:line` — finding the exact constant is the job of
whoever (or whatever) reads this.

Confidence reads `correlated`, not `caused`: attribution is by fault window, since one fault was
active when the assertion broke. `caused` requires trace data spanning the window, and trace
ingestion is not built yet ([`TBD-9`](SPEC.md)). The tool reports the confidence it actually has —
a per-hop causal chain would need those traces, so it is omitted rather than invented.

## Design

| Document | What it is |
|---|---|
| [`SPEC.md`](SPEC.md) | normative. 113 numbered requirements. Build against this. |
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
