# TortureU — Specification

**Status:** normative. This is the single source of truth for what gets built.
**Version:** 0 (pre-release; breaking changes allowed, must update this file first)
**Date:** 2026-08-08

---

## 0. How this document is used (SDD + TDD)

This spec is **normative**. `RESEARCH.md` is not — it holds the survey and the *why*, and where
the two disagree, this file wins.

Every requirement has a stable id (`R-AREA-n`) and uses RFC-2119 keywords: **MUST**, **MUST NOT**,
**SHOULD**, **MAY**.

The working loop is:

```
1  SPEC   state the requirement here first, with an id
2  RED    write a test naming that id, watch it fail
3  GREEN  minimal code to pass
4  CHECK  python3 check.py — verifies traceability
```

**Rules that make the two disciplines compose:**

- **R-PROC-1** — Production code **MUST NOT** be written before a failing test exists for it.
- **R-PROC-2** — Behaviour **MUST** be specified here before a test is written for it. If a test
  needs behaviour this file doesn't state, amend this file first.
- **R-PROC-3** — Each test **MUST** name the requirement it proves, in a comment of the form
  `// spec: R-DET-2`. `check.py` fails on a reference to a requirement that does not exist.
- **R-PROC-4** — Unresolved questions **MUST** appear in §11 as `TBD-n`, never as an assumption
  buried in code.

Traceability is reported by `check.py`, not assumed.

---

## 1. Scope

TortureU is a **single CLI** that torture-tests a backend from its own repo, and exposes the same
capability to coding agents through one MCP surface.

**R-SCOPE-1** — The tool **MUST** run against a local `docker-compose` stack with no Kubernetes
cluster required.

**R-SCOPE-2** — The tool **MUST** co-execute load and fault injection on a **single shared clock**.
This is the capability no surveyed tool provides off-Kubernetes and is the product's reason to exist.

**R-SCOPE-3** — The tool **MUST** present one front door to all 19 capability domains, at three
declared depths: `drive` (co-executed), `delegate` (config generated, handed off), `know` (named
with a trigger condition). Depth per tool is defined by `registry.yaml`.

**R-SCOPE-4** — The tool **MUST NOT** claim to execute `know`-tier tools. Output naming them **MUST**
show their tier.

**Out of scope for v0** (stated so it is not re-litigated): deterministic simulation testing,
linearizability checking beyond emitting a Porcupine history, DAST/pentest execution, managed
cloud load generators, Kubernetes-native execution.

---

## 2. Constraints

Locked. Changing one is a breaking change to the product, not an implementation detail.

### DC-1 — One vocabulary per question

**R-DC1-1** — The MCP surface **MUST NOT** expose a tool whose noun is a k6 concept (`script`,
`test`, `threshold`). TortureU's nouns are `experiment`, `fault`, `slo`, `verdict`.

**R-DC1-2** — `emit_k6_script` is the sole exception and **MUST** be an escape hatch: it returns a
script and performs no execution.

**R-DC1-3** — `init` **MUST NOT** unregister or modify another tool's MCP registration.

**R-DC1-4** — When a k6 MCP registration is detected, `init` **SHOULD** append a division-of-labor
note to the project's agent instructions file (`CLAUDE.md` / `AGENTS.md`).

### DC-2 — Default-deny egress

**R-DC2-1** — Egress classification **MUST** default to `deny`.

**R-DC2-2** — If any external host reachable from the stack is unclassified, `run` **MUST** abort
before generating load, **MUST** exit `3`, and **MUST** name every unclassified host.

**R-DC2-3** — The system under test **MUST** be placed in a network whose only egress path is the
TortureU proxy. Enforcement **MUST** be topological, not a policy check.

> Mechanism (verified feasible on vanilla Compose): put the SUT on a network marked
> `internal: true`, and dual-home the TortureU proxy on both that network and a normal bridge
> network. The SUT then has no route off-box except through the proxy — enforced by Docker's
> networking, with no cooperation required from the application.

**R-DC2-4** — A host classified `real` **MUST** carry a rate ceiling. Replay above 1× against a
`real` host **MUST** require an explicit flag.

**R-DC2-5** — Captured traffic **MUST** be secret-scrubbed on write. Scrubbing on replay only is
non-compliant.

---

## 3. Detection (`init`)

**R-DET-1** — Detection **MUST** read only `docker-compose.yml` and language manifests/lockfiles
(`go.mod`, `package.json`, `pyproject.toml`, `Gemfile`, `pom.xml`). It **MUST NOT** perform
general source analysis. *(D-3)*

This requirement is an **upper bound on what may be read**, not an obligation to support every
listed manifest. The v0 obligation is R-DET-14.

**R-DET-14** — v0 **MUST** support `go.mod`, `package.json`, and `pyproject.toml` — the three
ecosystems the registry's `lang:` predicates most depend on. `Gemfile` and `pom.xml` are
**TBD-7**. An unsupported manifest that is present **MUST** be reported as a gap (R-DET-7), never
silently ignored: a repo whose clients we cannot see yields weaker verdict candidates (D-9), and
the user has to know that.

**R-DET-2** — A compose service with an `image:` and no `build:` **MUST** be classified as a
dependency.

**R-DET-8** — A compose service with a `build:` **MUST** be classified as the system under test,
even when it also declares an `image:` — there, `image:` is the build's output tag, not a pull
reference. *(resolves TBD-3)*

**R-DET-3** — A dependency image that cannot be mapped to a known type **MUST** be reported as a
gap naming the image. It **MUST NOT** be assigned a guessed type. *(D-3 fail-loud)*

**R-DET-4** — Every dependency address and every external host found **MUST** be written to the
`egress:` block — in-compose dependencies as `class: internal`, anything else unclassified.
Unclassified entries block the next run per **R-DC2-2**.

**R-DET-5** — Detected client libraries **MUST** be recorded per dependency, for use as verdict
candidates. *(D-9)*

**R-DET-6** — Observability coverage (traces / metrics / logs present) **MUST** be detected and
reported, along with the maximum verdict confidence it permits. *(D-4)*

**R-DET-7** — `describe_system()` **MUST** return gaps explicitly. Silence about an unknown is
non-compliant.

### 3.1 Dependency type vocabulary

**R-DET-9** — The set of dependency types is closed and defined by this table. Any `dep:` predicate
in `registry.yaml` **MUST** appear here, and anything detection cannot map to a listed type
**MUST** become a gap per **R-DET-3**.

Source column says which **R-DET-1** input yields the type — `image` (compose service image),
`lockfile` (client library in a manifest), or either.

| Type | Source | Recognized by |
|---|---|---|
| `postgresql` | image, lockfile | `postgres*` · pgx, pq, psycopg, node-postgres |
| `mysql` | image, lockfile | `mysql*`, `mariadb*` · go-sql-driver, mysql2, PyMySQL |
| `redis` | image, lockfile | `redis*`, `valkey*` · go-redis, ioredis, redis-py |
| `mongodb` | image, lockfile | `mongo*` · mongo-driver, mongoose, pymongo |
| `kafka` | image, lockfile | `*kafka*`, `redpanda*` · sarama, kafkajs, confluent |
| `rabbitmq` | image, lockfile | `rabbitmq*` · amqp091, amqplib, pika |
| `nats` | image, lockfile | `nats*` · nats.go, nats.js |
| `elasticsearch` | image, lockfile | `elasticsearch*`, `opensearch*` |
| `cassandra` | image, lockfile | `cassandra*`, `scylla*` · gocql |
| `cockroach` | image | `cockroachdb*` |
| `etcd` | image, lockfile | `*etcd*` · etcd/client |
| `consul` | image, lockfile | `consul*` |
| `zookeeper` | image | `zookeeper*` |
| `oracle` | image, lockfile | `*oracle*`, `*oracledb*` |
| `minio` / `s3` | image, lockfile | `minio*` · aws-sdk s3, boto3 |
| `mqtt` | image, lockfile | `mosquitto*`, `emqx*` · paho |
| `aws` | image, lockfile | `localstack*` · aws-sdk, boto3 |
| `sqs` | lockfile | aws-sdk sqs client |
| `dynamodb` | lockfile | aws-sdk dynamodb client |
| `snowflake` | lockfile | snowflake-connector |
| `websocket` | lockfile | gorilla/websocket, ws, websockets |
| `smtp` | image, lockfile | `mailhog*`, `mailpit*` · net/smtp, nodemailer |
| `jms` | lockfile | javax.jms, jakarta.jms |
| `ldap` | lockfile | go-ldap, ldap3, spring-ldap |
| `soap` | lockfile | spring-ws, zeep, soap |

**R-DET-12** — Observability infrastructure **MUST** be recognized as its own classification: it is
neither a dependency (the SUT does not need it to serve requests) nor a gap (we know exactly what it
is). Recognized by image: `jaeger*`/`tempo*`/`zipkin*` → traces; `prom/prometheus*`/`victoriametrics*`
→ metrics; `grafana/loki*` → logs; `otel/opentelemetry-collector*` → traces and metrics.

> Without this, **R-DET-3**'s fail-loud rule would report every tracing backend as an unclassified
> gap, and `init` would demand the user classify their own observability stack as a torture target.

**R-DET-13** — A dependency whose type is `lockfile`-sourced in §3.1 **MUST** be recorded even when
no compose service corresponds to it — a managed service (SQS, DynamoDB, Snowflake) has no container
but is still a dependency that can fail. Such a dependency has clients but no address.

**R-DET-11** — Compose parsing **MUST** use `compose-spec/compose-go/v2`, not ad-hoc YAML
unmarshalling. Real compose files use `extends`, profiles, multiple files, `${VAR}` interpolation
and merge semantics; hand-rolled parsing silently misreads them, and a detection error is
indistinguishable from a system with no dependencies.

**R-DET-10** — A `dep:` predicate whose only source is `lockfile` **MUST NOT** be inferred from a
compose image, and vice versa. Mis-sourcing produces suggestions that never fire.

---

## 4. `torture.yaml` schema

**R-CFG-1** — Configuration **MUST** be a single flat file. No includes, no inheritance. *(D-1)*

**R-CFG-2** — Unknown top-level keys **MUST** be an error, not ignored. A typo'd `assert:` that
silently disables assertions is the worst failure this tool can have.

Top-level blocks: `version`, `target`, `egress`, `reset`, `load`, `faults`, `assert`, `fuzz`.

### 4.1 `target`

**R-CFG-3** — `target.compose` **MUST** be a path to a compose file. `target.service` **MUST** name
the system under test. `target.openapi` is optional and enables fuzzing.

### 4.2 `egress`

**R-CFG-4** — `egress.default` **MUST** accept only `deny` (v0). `egress.hosts` maps `host:port` to
`{class, ...}` where `class` ∈ `internal` | `mock` | `block` | `real`.

**R-CFG-5** — `class: mock` **MUST** carry `from:` ∈ `capture` | `spec`. `class: real` **MUST**
carry `max_rps`.

### 4.3 `load`

**R-CFG-6** — `load.model` **MUST** be `arrival_rate` (open model) in v0. A closed model **MUST NOT**
be offered, because it hides coordinated omission: as the system slows the offered load slows with
it, and the spike under test never occurs.

**R-CFG-7** — `load.stages` is an ordered list; each stage **MUST** carry a unique `phase` name.
Phase names are the anchors faults attach to.

**R-CFG-8** — Each stage **MUST** specify exactly one of `to:` (ramp) or `hold:` (steady), plus a
duration (`over:` or `for:`).

**R-CFG-9** — `load.scenarios[].flow[]` entries **MUST** be mappings with `method` and `path`.
Bare strings are non-compliant.

### 4.4 `faults`

**R-CFG-10** — Each fault **MUST** carry `at:`, `target:`, and `inject:`. `for:` is optional;
absent means "until end of run".

**R-CFG-11** — `at:` grammar — anchors survive edits to the load profile, which is the common edit:

```
at: <phase>                 anchor to the start of a declared load phase
at: <phase>+<duration>      anchor plus offset            e.g. peak+30s
at: t=<duration>            absolute from run start       e.g. t=90s   (escape hatch)
```

**R-CFG-12** — A `<phase>` in `at:` that is not declared in `load.stages` **MUST** be an error.

**R-CFG-13** — `target:` **MUST** name a detected service or a classified egress host.

**R-CFG-14** — v0 `inject:` verbs. Each fault's `inject:` **MUST** contain exactly one verb from
this table, optionally accompanied by that verb's modifiers. A second verb in the same `inject:`
**MUST** be an error — with two verbs present, a modifier like `workers` has no unambiguous owner.
Two simultaneous effects are expressed as two faults sharing an `at:`.

| Verb | Modifiers | Applies to | Effect |
|---|---|---|---|
| `latency` | `jitter` | network target | added delay |
| `down` | — | network target | connection refused |
| `bandwidth` | — | network target | rate cap |
| `slicer` | `delay` | network target | split packets |
| `error_rate` | `status` | mocked host | fraction of responses failed |
| `cpu`, `mem`, `io`, `fd` | `workers` | service | resource pressure |
| `cpu_limit`, `mem_limit` | — | service | cgroup ceiling |
| `pause`, `kill`, `graceful` | — | service | SIGSTOP / SIGKILL / SIGTERM |
| `poison_pill` | `count` | queue target | malformed message |
| `duplicate` | — | queue target | fraction redelivered |

**R-CFG-15** — `pause`, `kill`, and `graceful` **MUST** remain distinct verbs. They produce three
different client-observable behaviours and collapsing them loses the distinction.

### 4.5 `assert`

**R-CFG-16** — Assertions **MUST** use k6 threshold expression syntax verbatim for k6-visible
metrics. TortureU **MUST NOT** define its own metric DSL. *(D-2)*

**R-CFG-17** — A `promql:` entry **MUST** be accepted for signals k6 cannot observe (retry rate,
pool saturation, queue depth, data integrity).

**R-CFG-18** — A `sql:` entry **MUST** be accepted for run-scoped data-integrity invariants.

**R-CFG-19** — An empty or absent `assert:` block **MUST** be an error. A run that cannot fail is
not a test.

### 4.6 `reset`

**R-CFG-20** — Reset **MUST** run before each run by default, and **MUST** be skippable with
`--no-reset`. *(D-8)*

**R-CFG-21** — The reset command **MUST** be user-supplied, defaulting to
`docker compose down -v && docker compose up -d --wait`. TortureU **MUST NOT** implement database
snapshotting.

---

## 5. Execution

**R-EXE-1** — Load and faults **MUST** be driven from one clock, with fault times resolved against
observed phase boundaries. *(R-SCOPE-2)*

**R-EXE-2** — Reset **MUST** complete before load begins.

**R-EXE-3** — Egress enforcement **MUST** be active before the first request is generated.

**R-EXE-4** — If achieved throughput trails target by more than 5%, the verdict **MUST** carry a
warning that the load generator may be the bottleneck.

**R-EXE-8** — Phase anchors **MUST** resolve against stage-transition markers emitted by the
generated k6 script, not against TortureU's own wall clock. *(resolves TBD-4)*

> k6 exposes the running stage at runtime (`getCurrentStageIndex()`, plus `exec.scenario.progress`
> / `startTime`). Since we generate the script, it emits a marker on each transition; the fault
> scheduler subscribes to those. This removes clock skew between two processes from the "single
> shared clock" claim of **R-SCOPE-2** — the clock is k6's, and faults follow it.
>
> Note what this does *not* give us: markers follow the **declared** schedule, so if the generator
> falls behind, `peak` is announced while actual throughput is still climbing. That case is caught
> by **R-EXE-4** and **MUST** degrade finding confidence, never be silently reported as `caused`.

**R-EXE-9** — The generated k6 script **MUST NOT** fetch remote JavaScript at runtime (e.g.
`jslib.k6.io`). Helpers **MUST** be inlined. A default-deny egress harness (**DC-2**) that reaches
out to a CDN mid-run contradicts its own guarantee, and adds a supply-chain dependency to every run.

**R-EXE-6** — Every fault **MUST** be applied within container or container-network scope. TortureU
**MUST NOT** modify host `tc` rules, host cgroups, or host processes.

> Two reasons, and the second is the important one. **Portability:** Docker Desktop on macOS and
> Windows runs a Linux VM, so container-scoped cgroups, `netem` and signals behave natively, while
> anything host-scoped would be Linux-only. **Safety:** a crashed run must never leave a developer's
> own machine degraded. Container scope makes that structural rather than careful.

**R-EXE-7** — Supported platforms: Linux (native), macOS and Windows via Docker Desktop. On WSL,
`init` **SHOULD** warn — WSL runs cgroups v1 and v2 in a hybrid mode that misreports container
resource limits.

**R-EXE-5** — Faults **MUST** be torn down on exit, including on abort or panic. A crashed run
**MUST NOT** leave a proxy degrading a developer's stack.

---

## 6. Verdict

Full object schema in `VERDICT.md` §1, which is normative for field names.

**R-VER-1** — Every run **MUST** emit one verdict document.

**R-VER-2** — `status` **MUST** be one of `pass` | `fail` | `error` | `aborted`, where `fail` means
the system under test broke an assertion and `error` means TortureU itself failed. These **MUST NOT**
be conflated.

**R-VER-3** — Each finding **MUST** carry a `confidence` of `caused` | `correlated` | `ambiguous`,
assigned per-finding, not per-run. *(D-4)*

| Confidence | Requires |
|---|---|
| `caused` | traces spanning the fault window |
| `correlated` | exactly one fault active in the breach window |
| `ambiguous` | ≥2 candidate causes and no traces |

**R-VER-4** — Findings **MUST** report a candidate config surface (library + knobs), and **MUST NOT**
report a `file:line`. The last mile is the agent's. *(D-9)*

**R-VER-5** — Assertions that held **MUST** be listed, so "it passed" is distinguishable from
"it never ran".

**R-VER-6** — The verdict **MUST** include an egress audit listing mocked, blocked, real and
unclassified hosts. *(DC-2 evidence)*

**R-VER-7** — Exit codes:

| Code | Meaning |
|---|---|
| 0 | pass |
| 1 | fail — an assertion broke |
| 2 | error — TortureU or an adapter failed |
| 3 | aborted — unclassified egress, or reset failed |
| 4 | inconclusive — ran clean, all findings `ambiguous` |

**R-VER-8** — Code `4` **MUST NOT** be treated as success. A green that means "we couldn't tell" is
how a harness silently stops finding anything.

The trigger is stated as an algorithm so two implementers derive the same behaviour: exit `4`
when `status` is `fail` **and** every finding carries confidence `ambiguous`. A run with **no**
findings is not inconclusive — it is a pass (`0`). `inconclusive` is deliberately not a `status`
value: the run genuinely failed its assertions, and only the *attribution* is unusable, so the
distinction belongs in the exit code rather than in the document's status. *(closes the gap the
Task 3 review flagged)*

**R-VER-9** — Human output **MUST** be rendered from the same verdict document as machine output.
No second code path.

**R-VER-10** — k6 results **MUST** be ingested from its machine-readable JSON (`handleSummary()`
output, or `--out json` jsonlines for time series). The human CLI summary **MUST NOT** be parsed —
it is a presentation format with no stability guarantee.

> Grafana also publishes a JSON Schema for the end-of-test summary (`grafana/k6-summary`),
> intended as the stable automation contract. It is marked work-in-progress, so v0 targets
> `handleSummary()` and adopts the schema once it stabilises. *(TBD-5)*

---

## 7. CLI

**R-CLI-1** — Verbs for v0, in build order:

| Verb | Does |
|---|---|
| `init` | detect stack → `torture.yaml` + egress manifest |
| `run` | execute a scenario → verdict |
| `smoke` | constant-rate sanity check |
| `doctor` | resilience audit + registry coverage report |
| `mcp` | serve the MCP surface |
| `check` | contract compatibility (oasdiff, buf) |
| `emit` | generate a `delegate`-tier tool's config |
| `capture` | ingest traffic |
| `replay` | capture → load, subject to R-DC2-4 |

**R-CLI-2** — Every verb **MUST** be listed in `registry.yaml` as the `how:` of at least one tool.

**R-CLI-3** — `doctor` **MUST** report uncovered domains and `know`-tier suggestions with their
trigger condition, labelled by tier per **R-SCOPE-4**.

---

## 8. Resilience audit (`doctor`)

**R-AUD-1** — For each detected client library, `doctor` **MUST** report whether a timeout is
configured. Retries and circuit breakers are inert behind an infinite timeout.

**R-AUD-2** — `doctor` **MUST** flag retry configuration lacking a cap, backoff, or jitter.

**R-AUD-3** — `doctor` findings **MUST** be reported as hints, never as failures. They are static;
only a run proves them.

**R-AUD-4** — Each finding **SHOULD** name the experiment that would prove it.

**R-AUD-5** — The audit **MUST** inspect only known libraries' known construction sites.

"Construction site" means bounded source inspection: the call sites where a *known* client library
is constructed, and only those. **R-DET-1 bounds detection (`init`), not the audit.** The audit MAY
read source; it MUST NOT perform general source analysis, follow arbitrary control flow, or inspect
libraries absent from its table.

This distinction is load-bearing. Without it R-AUD-1/2 are unanswerable — knowing a repo imports
`pgx` says nothing about whether a timeout is set — and every finding degrades to "we did not check",
which is noise, not signal. The audit's entire value (RESEARCH.md §19: no resilience linter exists)
depends on actually reading the constructor.

**R-DC2-6** — Every egress function **MUST** fail closed on a class value it does not recognise,
independently of upstream validation. Classification, abort, and audit **MUST NOT** assume
`config.Parse` already rejected an unknown class.

A safety boundary that depends on a check somewhere else is not a boundary — it is a convention.
The concrete failure: an unrecognised class string is neither a known class nor literally
`"unclassified"`, so the abort check skips it and the audit's switch drops it into no bucket at
all, producing a **clean-looking audit for a host that was never classified**. That is the exact
"clean audit that isn't" outcome DC-2 exists to prevent, and it becomes reachable the moment
anyone weakens the parser in a refactor. *(raised by the Task 6 review)*

**R-DC2-7** — The project **MUST NOT** claim the DC-2 guarantee — in README, marketing, or CLI
output — until the topology overlay is applied by an executable run path and proven end to end.
Until then the parts exist but the guarantee does not, and claiming it would be the most damaging
possible misstatement for a tool whose positioning is that it cannot reach the internet.

**R-EXE-15** — Fault verbs are **owned by layers**, and a layer **MUST** pass over verbs it does not
own rather than rejecting them. Rejection is only correct for a verb no layer owns.

| Verb | Owner |
|---|---|
| `latency` `jitter` `down` `bandwidth` `slicer` `timeout` `reset_peer` | Toxiproxy (`internal/fault`) |
| `cpu` `mem` `io` `fd` `pause` `kill` `graceful` `cpu_limit` `mem_limit` | Docker/cgroup (`internal/fault`) |
| `error_rate` | mock provider (`internal/egress`, WireMock) — only legal on a `class: mock` host |
| `poison_pill` `duplicate` | broker producer (`internal/queuefault`) |

A rejection here is a defect, not caution: `torture.example.yaml` declares `error_rate`, so a layer
that errors on an unowned verb makes the project's own reference document unrunnable. This mirrors
R-CFG-16/17, where `internal/k6` passes over `promql:` asserts it does not own.

**R-EXE-17** — `poison_pill`'s `count` modifier defaults to **1** when omitted. One malformed
message is sufficient to block a partition indefinitely (RESEARCH.md §18), so the smallest
injection is both the realistic default and the least destructive one. Defaults that inject more
than the minimum make a fault harder to reason about and slower to clean up.

**R-EXE-18** — Queue-fault teardown **MUST** state that it can only stop further injection. It
cannot un-publish a poison pill already in the topic's log, nor retract a duplicate already
delivered or consumed. Unlike a network toxic, a published message is durable — the tool **MUST
NOT** imply reversibility it cannot deliver, in the same posture as **R-EXE-16**'s SIGKILL caveat.
*(Task 5b escalation)*

**R-EXE-16** — Teardown (**R-EXE-5**) **MUST** cover in-process panic and **SHOULD** cover SIGINT and
SIGTERM. `SIGKILL` cannot be trapped; the tool **MUST** document that limit rather than implying
protection it cannot provide, and **SHOULD** make faults recoverable on next start so a `SIGKILL`ed
run does not leave latency wired into a dependency forever.

**R-AUD-6** — Where the audit cannot determine a setting, it **MUST** say so explicitly and **MUST
NOT** assert absence. "Not determined" and "not configured" are different findings; conflating them
makes the audit cry wolf and users stop reading it. *(closes the gap the Task 9a implementer
escalated)*

---

## 9. MCP surface

**R-MCP-1** — Exactly five tools: `describe_system`, `propose_experiments`, `run_experiment`,
`explain_failure`, `emit_k6_script`.

**R-MCP-2** — `run_experiment` **MUST** be the only tool that executes anything.

**R-MCP-3** — `run_experiment` **MUST** return the verdict document of §6 unmodified.

**R-MCP-4** — `propose_experiments` **MUST** return `torture.yaml` fragments, not prose.

**R-MCP-5** — Tool names **MUST** satisfy **R-DC1-1**.

---

## 10. Licensing boundary

k6 is **AGPL-3.0**. Toxiproxy, Vegeta, WireMock and Schemathesis are permissive (MIT / Apache-2 /
MPL). The AGPL boundary is an implementation constraint, not a legal footnote.

**R-LIC-1** — TortureU **MUST** invoke k6 as a separate, unmodified process. It **MUST NOT** import
k6 Go packages, link against k6, or build an xk6 extension into its own binary — any of which makes
TortureU a derivative work under AGPL-3.

**R-LIC-2** — Generated k6 scripts and configuration are inputs to k6, not derivative works, and are
**MAY** be licensed freely.

**R-LIC-3** — If a k6 binary is redistributed with TortureU, it **MUST** be unmodified and carry its
own licence text.

**R-LIC-4** — Any future hosted/SaaS offering **MUST** be reviewed against AGPL-3 §13 before k6 runs
server-side on a user's behalf. Local CI and developer use carry no such obligation.

**R-LIC-5** — TortureU is **MIT** licensed. *(resolves TBD-3)* MIT and AGPL-3 **MUST NOT** be
combined in one distributed binary; the process boundary required by **R-LIC-1** is what keeps them
separate works. Containers are separate programs, so a bundled image **MAY** ship an AGPL k6
container alongside the MIT TortureU container, each under its own licence.

**R-LIC-6** — Every `drive`-tier tool's licence **MUST** be recorded in `registry.yaml` before an
adapter for it is written. A copyleft dependency discovered after integration is expensive; before
it is free.

---

## 11. Coverage

**R-COV-1** — `registry.yaml` is the source of truth for tool coverage. Counts in any other file
are derived and **MUST** be checked against it.

**R-COV-2** — Every registry entry **MUST** carry `tier`, `when`, and `how`.

**R-COV-3** — `when:` predicates **MUST** be namespaced (`dep:` `lang:` `spec:` `platform:` `has:`
`lacks:`) or the literals `always` / `never`. Alternatives **MUST** repeat the prefix
(`dep:kafka|dep:sqs`).

**R-COV-4** — Every predicate **MUST** be derivable from **R-DET-1** inputs alone.

**R-COV-5** — Detection **MUST** expose the facts every predicate namespace needs, so that no
registry entry is permanently unevaluable:

| Namespace | Fact | Derived from |
|---|---|---|
| `spec:openapi` | an OpenAPI/Swagger document exists | file presence |
| `spec:proto` | `.proto` files exist | file presence |
| `platform:k8s` | Kubernetes manifests or a Helm chart exist | file presence |
| `platform:aws` / `azure` | provider SDK in a manifest, or provider config present | manifest |
| `lacks:otel` | no OpenTelemetry client in any manifest, no collector in compose | manifest + compose |

`platform:aws` / `platform:azure` are **manifest-only**. An earlier draft also said "or provider
config present", but no spec-named config filename exists to check — satisfying it would require
either an invented filename heuristic or parsing Terraform/HCL, and the latter is the general source
analysis R-DET-1 forbids. Manifest SDK presence is the bounded, honest signal. *(Task 1 escalation)*

**R-COV-7** — `has:traffic-capture` is **not** a detection fact. It derives from `torture.yaml`,
which is not an R-DET-1 input, so detection **MUST NOT** attempt it. The predicate evaluator
**MUST** source it from configuration instead. Facts have owners: detection reports what the repo
*is*, configuration reports what the user *asked for*, and merging the two inside detection would
quietly widen R-DET-1's bound. *(Task 1 escalation)*

All are file- or manifest-presence checks and therefore inside R-DET-1. Before this requirement,
29 of 151 registry entries (19%) could never match, so `suggest` was silent for a fifth of the
catalogue — which defeats R-SCOPE-3.

**R-COV-6** — A predicate the system genuinely cannot evaluate **MUST** be reported as unevaluable,
never silently treated as false. A tool that fails to suggest is indistinguishable from a tool with
nothing to suggest, and only the second is honest.

---

## 12. Open (TBD)

- **TBD-1** — Verdict storage format for cross-commit trend tracking (SQLite / JSONL /
  Bencher-compatible). Blocked until there are runs worth comparing.
- **TBD-2** — Whether `emit` writes files or prints to stdout by default.
- **TBD-5** — Whether to adopt the `grafana/k6-summary` JSON Schema once it leaves
  work-in-progress, replacing our own `handleSummary()` shape.
- **TBD-6** — `Obs.MaxConfidence` when a repo has **no** observability infrastructure at all.
  Currently `""`. Candidates: `"correlated"` (we still schedule the faults, so time-window
  attribution holds — see D-4), or a distinct `"none"`. Raised by the Task 1 implementer.
- **TBD-7** — `Gemfile` and `pom.xml` manifest support (deferred from R-DET-14).
- **TBD-8** — **R-DC2-5** (secret-scrub captured traffic on write) has no capture/replay pipeline
  to attach to in v0, so there is no write path to scrub. It becomes binding the moment `capture`
  ships and **MUST** be implemented in the same change, never after: scrubbing retrofitted onto an
  existing corpus means the unscrubbed cassettes already exist. Raised by the Task 6 implementer.

---

## Traceability

`python3 check.py` reports which requirements have tests. It fails on a test citing a requirement
that does not exist here, and on any doc/registry disagreement.
