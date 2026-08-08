# TortureU

**Load testing tells you it got slow. TortureU tells you which dependency did it.**

One CLI that drives load and fault injection on the same clock **against a local
`docker-compose` stack — no Kubernetes** — and returns a single verdict naming what broke and why.

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

Chaos tools can inject the fault — but they need Kubernetes. Grafana's own `xk6-disruptor` puts
fault injection inside a k6 test, on k6's clock, which is the right idea — and it targets
Kubernetes Pods and Services. Chaos Mesh, Litmus and Testkube need a cluster too. Toxiproxy runs
anywhere but has no scheduler, so you drive it by hand.

Nothing lets you say this against a compose file on a laptop:

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
FAIL  checkout-spike  280s

  ✗ http_req_duration: p(95)<500 -> 4218ms
    caused by  pg_slow (postgres:5432)  [confidence: correlated]

    look at:  github.com/jackc/pgx/v5 MaxConns, MinConns, ConnConfig.ConnectTimeout

  ✓ http_req_failed: rate<0.01     0.003

  egress: 1 mocked, 1 blocked, 0 real          exit 1
```

That is the real output format, and its limits are visible in it:

- **`correlated`, not `caused`.** Attribution is by fault window — one fault was active when the
  assertion broke. `caused` needs trace data spanning that window, and trace ingestion is not
  built ([`TBD-9`](SPEC.md)). No per-hop causal chain is shown for the same reason: it would have
  to be invented.
- **`sql:` asserts read `not evaluated`.** There is no SQL execution path in v0, and an assert
  that was not evaluated is never reported as passing — a run whose asserts were all unevaluated
  exits `4` (inconclusive), never `0`. The same holds for `promql:` asserts when no Prometheus is
  configured.

Measured values (`4218ms`, `0.003`) come from k6's own summary; where a value genuinely cannot be
read it says `not measured`, which is deliberately distinct from `not evaluated`.

What it does give you is the *candidate config surface* — library plus knob names, never a
`file:line`. Finding the exact constant is the job of whoever, or whatever, reads this.

## Design

| Document | What it is |
|---|---|
| [`SPEC.md`](SPEC.md) | normative. 122 numbered requirements. Build against this. |
| [`RESEARCH.md`](RESEARCH.md) | the survey: 154 tools across 19 domains, and why each is driven, delegated or merely named |
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
