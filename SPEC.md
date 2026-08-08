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
- **R-PROC-4** — Unresolved questions **MUST** appear in §12 as `TBD-n`, never as an assumption
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

This requirement is an **upper bound on what may be read**. The v0 obligation is R-DET-14, which as
of TBD-7's resolution covers the whole list.

**R-DET-14** — v0 **MUST** support `go.mod`, `package.json`, `pyproject.toml`, `Gemfile`
(or `Gemfile.lock`), and `pom.xml` — every manifest R-DET-1 permits reading. *(closes TBD-7)*

A manifest that is present but whose declared dependencies cannot be read **MUST** be reported as a
gap (R-DET-7), never silently ignored, and the facts that manifest would have decided
(`platform:aws`, `platform:azure`, `lacks:otel`) **MUST** report as undetermined rather than false
(R-COV-6): a repo whose clients we cannot see yields weaker verdict candidates (D-9), and the user
has to know that. The live instance of this is a Maven **aggregator** `pom.xml`, whose dependencies
live in module `pom.xml`s outside any compose-declared directory.

Only **direct** dependencies count as clients. Ruby reads the `Gemfile`'s `gem` declarations
(falling back to `Gemfile.lock`'s `DEPENDENCIES` section, which is also direct-only); the
lockfile's resolved `GEM specs:` closure **MUST NOT** be read as clients, since a gem pulled in
transitively by a framework is not evidence that this service talks to that dependency.

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

The reported maximum **MUST NOT** be empty: a repo with no observability infrastructure at all
reports `correlated`, not `""` and not `none`. TortureU schedules the faults and k6 measures the
breach, so single-fault time-window attribution holds with zero cooperation from the target (D-4);
traces are what raise the ceiling to `caused`. *(closes TBD-6)*

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
| `postgresql` | image, lockfile | `postgres*` · pgx, pq, psycopg, node-postgres, pg (gem), org.postgresql:postgresql |
| `mysql` | image, lockfile | `mysql*`, `mariadb*` · go-sql-driver, mysql2, PyMySQL, mysql-connector-j |
| `redis` | image, lockfile | `redis*`, `valkey*` · go-redis, ioredis, redis-py, redis (gem), jedis, lettuce |
| `mongodb` | image, lockfile | `mongo*` · mongo-driver, mongoose, pymongo, mongoid, mongodb-driver |
| `kafka` | image, lockfile | `*kafka*`, `redpanda*` · sarama, kafkajs, confluent, ruby-kafka, kafka-clients, spring-kafka |
| `rabbitmq` | image, lockfile | `rabbitmq*` · amqp091, amqplib, pika, bunny, amqp-client, spring-rabbit |
| `nats` | image, lockfile | `nats*` · nats.go, nats.js |
| `elasticsearch` | image, lockfile | `elasticsearch*`, `opensearch*` · elasticsearch (gem), elasticsearch-java |
| `cassandra` | image, lockfile | `cassandra*`, `scylla*` · gocql, cassandra-driver, java-driver-core |
| `cockroach` | image | `cockroachdb*` |
| `etcd` | image, lockfile | `*etcd*` · etcd/client, jetcd |
| `consul` | image, lockfile | `consul*` |
| `zookeeper` | image | `zookeeper*` |
| `oracle` | image, lockfile | `*oracle*`, `*oracledb*` |
| `minio` / `s3` | image, lockfile | `minio*` · aws-sdk s3, boto3, aws-sdk-s3 (gem), awssdk:s3 |
| `mqtt` | image, lockfile | `mosquitto*`, `emqx*` · paho (incl. org.eclipse.paho), mqtt (gem) |
| `aws` | image, lockfile | `localstack*` · aws-sdk, boto3 |
| `sqs` | lockfile | aws-sdk sqs client (incl. aws-sdk-sqs gem, awssdk:sqs) |
| `dynamodb` | lockfile | aws-sdk dynamodb client (incl. aws-sdk-dynamodb gem, awssdk:dynamodb) |
| `snowflake` | lockfile | snowflake-connector, snowflake-jdbc |
| `websocket` | lockfile | gorilla/websocket, ws, websockets |
| `smtp` | image, lockfile | `mailhog*`, `mailpit*` · net/smtp, nodemailer |
| `jms` | lockfile | javax.jms, jakarta.jms, spring-jms |
| `ldap` | lockfile | go-ldap, ldap3, spring-ldap, net-ldap |
| `soap` | lockfile | spring-ws, zeep, soap, savon |

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

### 5.1 Co-driven load sources (`--db-load`, `--fuzz`)

`registry.yaml` registers two further `drive`-tier tools whose front door is a `run` flag:
`pgbench` (`when: dep:postgresql`, `how: tortureu run --db-load`) and `schemathesis`
(`when: spec:openapi`, `how: tortureu run --fuzz`). `drive` is the whole claim (R-SCOPE-3): both
are **co-executed on the run's own clock**, folded into the one verdict. Emitting a script and
handing it off would be `delegate`, and the registry does not say `delegate`.

**R-EXE-26** — `run --db-load` **MUST** co-execute `pgbench` against the detected PostgreSQL
dependency *while* the HTTP load and the faults run, so the database is saturated independently of
the application (R-SCOPE-2, R-EXE-1). Its lifecycle **MUST** bind to the run clock exactly as
faults do:

- it **MUST** start on k6's first phase marker (**R-EXE-8**) — not on TortureU's own wall clock,
  and not before load exists, so "under load" is a fact rather than a hope;
- it **MUST** be terminated when the load ends, and on abort, signal or panic, through the **same**
  teardown path faults use (**R-EXE-5**, **R-EXE-16**). A crashed run **MUST NOT** leave pgbench
  hammering a developer's database;
- it **MUST** carry its own upper duration bound, so a pgbench that outlives the orchestrator that
  spawned it still stops by itself.

Refusals — this flag **MUST NOT** silently no-op, which is this project's worst failure mode:

- **no trigger**: if detection reports no dependency of type `postgresql` (R-DET-9), the run
  **MUST** fail with `status: error` (exit `2`), naming the absent trigger condition;
- **no credentials**: the connection string is supplied by `-db-url` and by nothing else. TortureU
  **MUST NOT** guess a user, password, host, port or database name, nor read one out of the
  compose file. Absent `-db-url` **MUST** be an error naming the flag;
- **no binary**: `pgbench` absent from `PATH` **MUST** be reported with an install hint in the
  manner of **R-CLI-5**, never as an obscure failure;
- every refusal above **MUST** happen *before* reset and before any load starts. Discovering the
  flag was unusable after a run has already perturbed the stack teaches the user nothing.

`pgbench`'s own initialization (`pgbench -i`) **creates and drops tables named `pgbench_*`** in the
target database. That is a write against the caller's data, so the flag's help text **MUST** say
so; it is not something a user may discover from the effects.

Results: what the DB load achieved (tps, client count, duration, and whether it was cut short by
the load ending first) **MUST** appear in the verdict as an artifact — a run that claims DB
pressure has to be able to show it. pgbench failing to run at all (unreachable database, bad DSN,
missing binary) is **TortureU** failing: `status: error` (**R-VER-2**). A SUT that degrades under
DB pressure is a *result* and surfaces through the run's own assertions, never as an `error`.

**R-EXE-27** — `run --fuzz` **MUST** co-execute `schemathesis` against the system under test's
OpenAPI specification, on the same clock and with the same lifecycle binding **R-EXE-26** states
(start on the first phase marker; terminated at load end, abort, signal or panic through the shared
teardown path; own upper duration bound). Fuzzing *under load and faults* is the point: the cheap
500s a fuzzer finds against an idle service are not the interesting ones.

Refusals, in the same shape as **R-EXE-26**:

- **no trigger**: `spec:openapi` false in detection's `Coverage` (R-COV-5) **MUST** be an error
  naming the absent trigger, never a silent skip;
- **no spec path**: the document fuzzed is `target.openapi` from `torture.yaml`, or `-fuzz-spec`.
  It **MUST NOT** be guessed by scanning for conventional filenames — a fuzzer pointed at the
  wrong document reports confident nonsense. The URL fuzzed is `target.base_url`, equally
  un-guessable and equally an error when absent;
- **no binary**: `schemathesis` (or its `st` alias) absent from `PATH` **MUST** carry an install
  hint per **R-CLI-5**;
- all three **MUST** be checked before reset and before load.

Findings: each failing operation schemathesis reports **MUST** become a finding in the verdict
(**R-VER-1**). **R-VER-2's distinction is load-bearing here**: a fuzzer finding a `500` is the
system under test breaking (`fail`, exit `1`), *not* TortureU failing — schemathesis exiting
non-zero **because it found failures** **MUST NOT** be reported as `status: error`. Only a
schemathesis that could not run at all (missing binary, unparseable spec, unreachable target) is
`error`.

The two are distinguishable in schemathesis's own machine-readable output, and the implementation
**MUST** use that rather than its exit status alone: its JUnit report emits `<failure>` for a
response that broke a check (a *result*) and `<error>` for a case it could not execute at all
(network failure, so *no* result) — while the process exits `1` for either. A run whose report is
all `<error>` and no `<failure>` **MUST** be `status: error`; a run carrying both reports the
failures as findings **and** warns that some cases could not be executed, never silently dropping
either half.

Confidence per **R-VER-3**, assigned per finding:

| Run declared | Confidence | Why |
|---|---|---|
| no faults | `correlated` | the fuzzer's own request is the sole candidate cause, and it is reported verbatim |
| ≥1 fault | `ambiguous` | the injected fault is a second candidate cause and no traces exist to separate them |

A fuzz pass cut short by the load ending first **MUST** report what it found **plus** a warning
that it was cut short. Reporting a truncated fuzz run as a clean one is the silent-omission failure
this project rejects everywhere.

*(both proposed by the implementer and specified before citation, per R-PROC-2; the `pgbench` and
`schemathesis` registry entries named these flags with nothing behind them)*

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

**R-VER-11** — The verdict **MUST** carry the observability coverage detection reported for this
repo (**R-DET-6**) and the maximum confidence it permits, in `VERDICT.md` §1's `observability`
block. A verdict that omits it renders the zero value — `traces/metrics/logs` all false and an
empty ceiling — for every run, which is a *false* statement about repos that do have tracing, and
silence about the ceiling for repos that do not. Both are the silent omission this project rejects,
and the ceiling is exactly what tells a user why their findings say `correlated` and not `caused`.

*(the field existed in `VERDICT.md` §1 and in `internal/verdict` and was never populated by
`internal/run`; specified here before the fix, per R-PROC-2)*

**R-VER-9** — Human output **MUST** be rendered from the same verdict document as machine output.
No second code path.

**R-VER-10** — k6 results **MUST** be ingested from its machine-readable JSON (`handleSummary()`
output, or `--out json` jsonlines for time series). The human CLI summary **MUST NOT** be parsed —
it is a presentation format with no stability guarantee.

**We take k6's measurements and compute our own verdicts.** Metric *values* come from k6; threshold
*pass/fail* is recomputed by parsing the threshold expression we generated and comparing it against
the measured value. This is not distrust of k6 as a load generator — its measurements are the whole
reason we drive it — but a verdict is the thing this tool exists to produce, so it is the thing we
must be able to derive and defend ourselves.

The reason is concrete. k6 0.54.0's `--summary-export` reports **every threshold as `false` on any
arrival-rate executor regardless of the measured value**, and **R-CFG-6 permits only** arrival-rate,
so the single executor family we may use is the one where its export is unusable. Worse,
`--summary-export`'s writer recomputes independently and **silently discards** whatever
`handleSummary()` returned — verified by overwriting `ok:true` before returning and observing no
effect on disk. Depending on any of k6's booleans therefore makes our verdict hostage to a
behaviour that varies by executor and by version, without a signal when it changes.

E1 measured the cost of not doing this: a control backend with **no defect at all** produced two
findings in three of four runs. *(E1 → Task 4, 2026-08-08)*

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

**R-CLI-11** — `init --ci [provider]` **MUST** write a CI pipeline that runs `tortureu run` and
treats **R-VER-7**'s exit codes `0`–`4` as the contract. `provider` is `github` (default) or
`gitlab`; any other value **MUST** error listing what is supported, in the manner of **R-CLI-8**.

The generated pipeline **MUST**:

- branch on the exit code and report each of `0`–`4` **distinctly**, by the meaning **R-VER-7**
  gives it, and **MUST NOT** collapse them into "non-zero". "The build went red" does not tell a
  reviewer whether their service broke (`1`), the harness broke (`2`), the run never started
  (`3`), or nothing could be attributed (`4`) — and those four demand four different responses;
- **fail the build** on `1`, `2`, `3` **and** `4` — `4` in particular, per **R-VER-8**: a green
  that means "we couldn't tell" is how a harness silently stops finding anything. No
  `continue-on-error` / `allow_failure` may be emitted for the run step;
- propagate the code itself rather than a substitute, so the distinction survives into the job's
  own status;
- treat a code outside `0`–`4` as an unexpected failure and fail, never as a pass. **R-COV-6**'s
  rule applies: a result the pipeline cannot interpret is reported as uninterpretable, not
  silently treated as success.

`--ci` is a **mode**, not a modifier: it writes the pipeline file only, and **MUST NOT** run
detection or write `torture.yaml`. The two artefacts have different lifetimes — `torture.yaml` is
regenerated as the stack changes, the pipeline is written once — and a repo may want CI wiring
without re-deriving a config it has already edited.

It **MUST NOT** overwrite an existing file at the destination path (`.github/workflows/tortureu.yml`
or `.gitlab-ci.yml`, overridable with `-ci-out`). It **MUST** refuse, naming the path, and exit `2`.
A pipeline file is hand-edited after generation — runner labels, secrets, the install step — and
silently replacing it destroys work `init` cannot regenerate. This is deliberately stricter than
`init`'s existing treatment of `torture.yaml`, which does overwrite; that asymmetry is a known
inconsistency, not an oversight, and changing `torture.yaml`'s behaviour is out of scope here.

*(behaviour proposed by the implementer and specified before citation, per R-PROC-2; the
`github-actions` and `gitlab-ci` registry entries named this flag with nothing behind it)*

**R-CLI-9** — `capture` **MUST** scrub credential-shaped data — auth headers, cookies, bearer
tokens, credential-shaped body and query fields — from an exchange **before** it is written to the
cassette. No code path may persist an unscrubbed byte. *(satisfies R-DC2-5, closes TBD-8)*

The test for this **MUST** read the written file back from disk and assert the credential is absent.
Asserting on an in-memory value proves the struct was scrubbed, not the artefact — and the artefact
is what gets committed to someone's repo.

**R-CLI-12** *(proposed)* — `capture -engine <name>` selects the capture engine. `proxy` (the
default) is the built-in scrubbing proxy R-CLI-9 governs. `keploy` is a **delegate**-tier handoff
(R-SCOPE-3): TortureU **MUST** generate keploy's command and configuration for the detected system
and hand off, and **MUST NOT** run keploy, wrap its output, or drive it on TortureU's clock. Keploy
captures with eBPF and produces its own tests plus auto-mocks; reimplementing that is the
"integrate, never reimplement" line `registry.yaml`'s keploy entry draws.

An unrecognised `-engine` value **MUST** error listing the supported engines and exit `2`. It
**MUST NOT** fall back to the default engine: a silent fallback would leave the user believing
keploy ran and produced eBPF-derived mocks when what actually ran was our own HTTP proxy.

The handoff **MUST NOT** guess keploy's required inputs. Keploy's `record` mode has exactly one
hard requirement — `-c/--command`, the command that starts the application — and for a
docker-compose application it also needs `--container-name`, which must match the SUT service's
`container_name:` in the compose file. Where the compose file does not state a `container_name:`,
or where detection cannot name the SUT service, `capture -engine keploy` **MUST** refuse and say
which input is missing and where to state it, in the manner of `internal/emit`'s `noDepNote` — a
guessed container name produces a keploy run that records nothing and reports success.

Because keploy is a delegate, absence from `PATH` is not an error of ours: it **MUST** be reported
with an install hint in the manner of **R-CLI-5**, alongside the generated command, which is still
correct on a machine that has not installed keploy yet.

*(behaviour proposed by the implementer and specified before citation, per R-PROC-2; the keploy
registry entry named `--engine` with nothing behind it — see TBD-13 for what this environment could
not verify)*

**R-CLI-13** *(proposed)* — A cassette entry **MUST** carry the absolute call and return instants of
the exchange as `call_ns` and `return_ns`: integer nanoseconds on a single monotonic timeline whose
origin is the start of the recording session, and which is meaningful **only within one cassette**.
Wall-clock timestamps are deliberately not used — a clock step during a capture would reorder
operations that did not reorder.

This exists because a linearizability check (the porcupine entry in `registry.yaml`) is defined
entirely by which operations overlapped in real time. A per-entry `seq` and `duration_ms` cannot
express overlap, and reconstructing call/return instants from a sequence number would fabricate the
very fact being checked.

`duration_ms` **stays**, derivable though it now is from the pair: it is what a human reads in a
`git diff` of a cassette, and removing it would break nothing but would cost the format its
readability for no gain.

Both fields are additive and optional on read. A cassette written before this requirement has
neither, and `replay` (R-CLI-10) **MUST** continue to drive it unchanged — replay is sequential and
consults neither field, so an old cassette replays identically rather than being misread or
refused. A consumer that *does* need the instants (a linearizability checker) **MUST** treat a
zero/absent pair as "this cassette does not carry a history", never as "everything happened at time
zero".

*(behaviour proposed by the implementer and specified before citation, per R-PROC-2)*

**R-CLI-10** — `replay` **MUST** drive a cassette written by `capture` as load against `-target`,
honouring `-multiplier` and `-allow-real-traffic` through the **same** `internal/egress`
`R-DC2-4` guard `run` uses. Reimplementing that guard would create a second, weaker path to the
same dangerous capability — replay above 1x against a real host is exactly what turns a test into
someone else's outage.

*(both proposed by the implementer and specified before citation, per R-PROC-2)*

**R-CLI-8** — `emit <tool>` **MUST** generate a runnable command or config for a `delegate`-tier
tool from `torture.yaml`, **to stdout by default** so it composes (`tortureu emit pumba > chaos.sh`).
*(closes TBD-2)*

It **MUST** reuse `internal/config` and `internal/fault` rather than re-deriving fault semantics — a
second translation of the same verbs would drift from the one the run actually uses, and the two
would disagree silently. It **MUST** report, per fault, any verb it does not translate rather than
dropping it: a config missing a fault the user asked for is the silent-omission failure this project
rejects everywhere. An unrecognised tool name **MUST** error listing what `emit` supports.

`emit` performs **no scheduling** against the k6 phase clock. Timing is the caller's — that is what
`delegate` tier means (R-SCOPE-3: real output, separate timing), and claiming otherwise would make
it indistinguishable from `drive`.

*(behaviour proposed by the implementer and specified before citation, per R-PROC-2)*

**R-CLI-7** — `check contracts` **MUST** detect `spec:openapi` / `spec:proto` via detection's
`Coverage` facts and invoke the corresponding tool — `oasdiff` or `buf breaking` — against a
caller-supplied `-baseline` (a git ref or file path). The baseline **MUST NOT** be guessed: a
missing one is an error, because silently comparing against the wrong baseline produces a
confident, wrong answer.

These are **delegate**-tier tools (R-SCOPE-3): we detect what applies and hand off. We do not
reimplement them, and a tool absent from `PATH` **MUST** be reported with an install hint in the
manner of **R-CLI-5**, never as an obscure failure.

Exit codes follow **R-VER-7**, and the distinction **R-VER-2** draws applies: a breaking change is
a *result* (`1`), not a tool error (`2`). Reporting a real finding as our own failure would send a
user to debug TortureU instead of their API.

*(behaviour proposed by the implementer and specified before citation, per R-PROC-2)*

**R-CLI-6** — `smoke` **MUST** drive a constant request rate against `-url` for a fixed short
duration with no `torture.yaml`, and **MUST** reach a SUT isolated by **R-DC2-3**'s internal-only
network the same way `run`'s load path does — a direct dial first, falling back to a
container-network-namespace join.

It **MUST** report requests sent, success count and rate, and p50/p95/p99 latency, and **MUST NOT**
produce a verdict document, findings, or attribution: those are `run`'s, and a second producer of
them would be the duplicate-source-of-truth **R-VER-9** forbids.

Exit codes follow **R-VER-7**: `2` when zero requests were attempted (the tool failed), `1` when
requests were sent and the success rate fell below `-min-success-rate` (the system failed), `0`
otherwise. Codes `3` and `4` do **not** apply — `smoke` performs no egress classification, no reset,
and no per-finding confidence — and **MUST NOT** be repurposed for other meanings.

*(behaviour proposed by the implementer and specified before citation, per R-PROC-2)*

**R-CLI-5** — `doctor` **MUST** report whether the tools a run needs (`k6`, `docker`,
`docker compose`) are present on this machine, and `init` **MUST** warn about any that are missing
without failing — writing a config is still useful on a machine that cannot yet run.

Presence is what can be checked, so presence is what is claimed: a found binary is reported as
found, never as working. Missing entries **MUST** carry an install hint.

Without this the first failure arrives late: a user runs `init`, edits a config, runs `run`, and
only then learns a prerequisite was absent the whole time. We do not bundle k6, so "k6 not on
PATH" is the most likely first-run outcome for a new user, and for an audience whose stated
barrier is fear of breaking things, a late failure after two steps of setup is where they give up.
*(behaviour shipped in Task 8 and specified after the fact — the Task 8 implementer correctly
escalated that no requirement covered it rather than inventing one)*

**R-CLI-4** — `init` **MUST** write a `torture.yaml` that `run` accepts, including a minimal
starter `load:` and `assert:` clearly marked as a starting point to edit.

Detection cannot infer scenarios (R-DET-1 forbids reading source), so the starter is deliberately
generic — a low-rate ramp against the detected SUT with a conservative latency and error-rate
assert. It **MUST NOT** fabricate specifics it cannot know, such as endpoint paths beyond `/`, and
**MUST** carry a comment saying so.

The alternative — emitting a file that `run` rejects — makes the first experience an error
message. For a tool whose adoption barrier is fear (RESEARCH.md: 62% cite fear of causing
disruption), a first run that fails to start is the worst possible introduction. *(found by
running `tortureu init` on a synthetic repo)*

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

**SATISFIED** — `internal/run/dc2_enforcement_test.go`. A real compose stack is brought up through
`ComposeTopologyApplier.Apply`; `docker exec` inside the running SUT proves it reaches a classified
host through the proxy and **cannot** reach an arbitrary external address. A committed negative
control flips only the `internal` flag on the same stack and asserts the external address *becomes*
reachable — so the positive test is measuring isolation rather than an unrelated routing accident,
and a refactor that stops applying isolation fails CI.

The claim held back through three review rounds that each found it unearned: topology generated but
never applied; `docker compose config` tested while `up` never ran, with proxies created lazily so
nothing was on the path; and enforcement proven but its regression path untested. **A guarantee
whose regression path has no automated test is asserted, not proven.**

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

**R-CFG-23** — Numeric modifier values **MUST** be range-checked at parse time, and the error
**MUST** name the fault, the modifier, and the legal range:

| Modifier | Legal range |
|---|---|
| `duplicate` | `0.0 … 1.0` — a **proportion** of messages, not a multiplier |
| `error_rate` | `0.0 … 1.0` |
| `count` (`poison_pill`) | integer ≥ 1 |
| `workers` | integer ≥ 1 |

`duplicate: 5` is the motivating case: read as a rate it means 500%, which is meaningless, and
nothing rejected it. A fault whose magnitude is nonsense produces a run whose verdict is nonsense,
and the user has no way to tell that is what happened.

Owning layers **MUST** re-check independently rather than trusting the parser — the same
defence-in-depth rule as **R-DC2-6**. Config validation fails fast with a good message; the owning
layer's check is what holds when a caller bypasses the parser. *(Task 5b review)*

**R-EXE-17** — `poison_pill`'s `count` modifier defaults to **1** when omitted. One malformed
message is sufficient to block a partition indefinitely (RESEARCH.md §18), so the smallest
injection is both the realistic default and the least destructive one. Defaults that inject more
than the minimum make a fault harder to reason about and slower to clean up.

**R-EXE-18** — Queue-fault teardown **MUST** state that it can only stop further injection. It
cannot un-publish a poison pill already in the topic's log, nor retract a duplicate already
delivered or consumed. Unlike a network toxic, a published message is durable — the tool **MUST
NOT** imply reversibility it cannot deliver, in the same posture as **R-EXE-16**'s SIGKILL caveat.
*(Task 5b escalation)*

**R-EXE-25** — `pause`, `kill` and `graceful` are distinct at the **signal and exit-code** layer
(`SIGSTOP`; `SIGKILL`, exit 137; `SIGTERM`, exit 0), **not** at the client-visible TCP layer. A
client sees `EOF` for both `kill` and `graceful`.

An earlier draft claimed three distinct *client-visible* failure classes, and B1 measured that as a
`kill` MISS. Investigation with real Docker across three topologies — published port, shared network
namespace, and an unread-data control — found `io.EOF` in every case and never `ECONNRESET`: Linux
closes an idle drained socket with an orderly FIN on process death regardless of which signal killed
the process, and the applier cannot reach the target's own socket options to force an abortive close.

**For an actual RST, use Toxiproxy's `reset_peer`** — the network-layer mechanism that already
exists for exactly this. The claim is corrected rather than the measurement widened; evidence is
committed as `internal/run/kill_rst_test.go`, which fails loudly if the behaviour ever changes.
*(B1 → Task 7 investigation, 2026-08-08)*

**R-EXE-24** — `jitter` accompanies `latency` and adds a **uniform** random offset in
`[-jitter, +jitter]`, not Gaussian noise with standard deviation `jitter`. That is Toxiproxy's
semantics and we adopt it rather than translating, so the observed standard deviation of the delay
is `jitter / sqrt(3)` — about `28.9ms` for `jitter: 50ms`.

Stated because B1 initially recorded `jitter` as a MISS against a tolerance that assumed
`sigma = jitter`. The measured `27.95ms` matched uniform semantics almost exactly, so the
tolerance was wrong, not the tool. A benchmark that misstates the distribution it is measuring
manufactures a defect. *(B1, 2026-08-08)*

**R-EXE-21** — `error_rate`'s injected status defaults to **500** when unstated. The verb models a
dependency failing, and a server error is what a client's retry, timeout, and circuit-breaker paths
are written against — a 4xx would exercise validation handling instead, which is a different test.

**R-EXE-22** — Where a mock provider has no native probabilistic primitive, `error_rate` **MAY** be
approximated by a deterministic cycle, and the implementation **MUST** state its resolution. A
20-state cycle gives 5% resolution: `0.15` is exact (3 of 20), `0.17` is not. A rate finer than the
resolution **MUST** be reported as approximated rather than silently rounded — a user who asked for
17% and got 15% must be able to see that in the verdict, since they may be reading the result as
evidence about a threshold.

**R-EXE-23** — A `duplicate` implementation that consumes and republishes **MUST NOT** consume its
own republished messages. Without a guard the loop compounds: every duplicate becomes a source of
further duplicates, and the injected rate silently becomes unbounded amplification rather than the
proportion the user asked for. *(all three raised by Task 10)*

**R-EXE-20** — Faults targeting a `class: internal` dependency **MUST** be intercepted. Internal
dependencies are the *primary* fault target, not an edge case: `torture.example.yaml`'s flagship
faults (`pg_slow`, `redis_dies`) both target them, and the capability the whole product exists to
provide — "5k rps while Postgres gains 300ms" — is exactly this.

Interception **MUST NOT** rely on aliasing the proxy to the dependency's own service name, which
collides with that service's DNS identity. The workable shape is to move the real dependency aside
and give the proxy the name the SUT already resolves: rename the backend service (or its alias) to
e.g. `db-tortureu-backend`, give the Toxiproxy container the network alias `db`, and have the proxy
forward to the renamed backend. The SUT's configuration is untouched — it still connects to `db` —
which matters because requiring users to edit their app config to be testable defeats the premise.

Until this holds, `run` **MUST** fail loudly for a fault targeting an internal dependency rather
than executing a run in which the fault silently never reaches the traffic (**R-EXE-19**'s rule
applied to interception rather than routing). *(escalated by Task 7 after DC-2 external enforcement
was proven)*

**R-EXE-19** — A verb passed over under **R-EXE-15** **MUST** be routed to its owning layer. Silently
skipping it is forbidden, and if no owning layer is wired the run **MUST** fail rather than proceed.

Pass-over means *"not mine, give it to the owner"* — never *"nothing to do"*. A declared fault that
never fires is the worst failure this tool can produce: the run completes, the verdict reads
`pass`, and the user concludes their system withstood a fault that was never applied. Unlike a
crash, nothing signals that the result is meaningless.

This is the mirror of **R-EXE-15**: that requirement stops a layer erroring on a verb it does not
own; this one stops the same verb vanishing. *(raised by the R-CFG-23 re-review, which found
`error_rate` validated but never invoked — `internal/run`'s scheduler skipped every passed-over
fault)*

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

**R-MCP-7** — The MCP surface **MUST** be reachable over a transport an assistant can actually
connect to: newline-delimited JSON-RPC 2.0 on stdio, supporting `initialize`, `tools/list` and
`tools/call`. Every failure — parse error, unknown method, unknown tool, bad arguments, tool error
— **MUST** return a JSON-RPC error rather than panicking or closing the stream.

`run_experiment` executes a real run and can take minutes. The server processes one request at a
time and does **not** implement a progress protocol, so a long call blocks the loop until it
completes. That **MUST** be documented on the server and in the tool's own description, so the
behaviour reads as expected rather than hung — an assistant that cannot tell "working" from
"wedged" will abandon the call and retry, which starts a second Docker stack.

*(behaviour shipped in Task 9b, specified after the fact — the implementer correctly escalated
that no requirement governed the transport rather than inventing one)*

**R-MCP-6** — `describe_system()` **MUST** include registry coverage and tier-labelled suggestions
for the detected system, so an agent reaches the `delegate` and `know` tiers through the MCP
surface, not only a human through `doctor`.

Without this, "all in one place" holds for humans and fails for agents: the five MCP tools reach
only the 28 `drive`-tier tools, leaving the other 123 visible exclusively at the CLI. Since agents
are half this project's audience (DC-1), that is half the claim missing.

It goes in `describe_system` rather than a sixth tool because **R-MCP-1** fixes the surface at five,
and because coverage is a fact *about the system* — the same noun `describe_system` already owns
(it reports observability coverage for exactly this reason). Suggestions **MUST** carry their tier
(**R-SCOPE-4**): an agent must never be told we execute something we only name. *(raised by the
Task 9b review's R-SCOPE-4 note)*

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

**R-COV-8** — `registry.yaml` **MUST** be embedded in the binary (`go:embed`), not read from the
working directory. `doctor` and `suggest` are the front door to the `delegate` and `know` tiers, so
a registry loaded from disk means they work only inside TortureU's own repo and fail everywhere a
user would actually run them — which is the entire point of shipping a single static binary (D-6).
*(found by running the built CLI against a synthetic repo: `doctor: read registry: open
registry.yaml: no such file or directory`)*

**R-COV-6** — A predicate the system genuinely cannot evaluate **MUST** be reported as unevaluable,
never silently treated as false. A tool that fails to suggest is indistinguishable from a tool with
nothing to suggest, and only the second is honest.

---

## 12. Open (TBD)

- **TBD-1** — Verdict storage format for cross-commit trend tracking (SQLite / JSONL /
  Bencher-compatible). **NARROWED 2026-08-09, still open.** The Bencher-compatible option is now
  built (`tortureu emit bencher`, R-CLI-8) and it turns out **not to be a third storage format at
  all**: Bencher Metric Format is a *projection* of the verdict computed at emit time, and the
  history lives in Bencher's own server, so nothing new is stored on our side. Verified against
  the real CLI (bencher 0.6.11) — a verdict document goes in, a report Bencher accepts comes out.
  Two consequences worth keeping.

  First, the choice that remains is only about **local** comparison (SQLite / JSONL), i.e. what a
  repo with no Bencher project gets. That is still blocked on runs worth comparing, so it stays
  open.

  Second, and more useful: implementing this exposed that the format is **not the binding
  constraint**. Every trend needs a per-run anchor, and `verdict.Commit` — VERDICT.md §1's
  `commit` field, the one it labels "for §12 trend tracking" — is **written by no producer in this
  codebase**; `internal/run` never sets it. Bencher's own `--hash` additionally rejects anything
  but a full 40-character git hash, so VERDICT.md's own example value (`a3f19c2`) is refused. Any
  storage format chosen today would therefore be a store of unanchored rows. Populating the commit
  anchor is the prerequisite, and it is upstream of this decision rather than part of it.

- **TBD-14** — What SHAPE a `sql:` assertion (R-CFG-18) is. R-CFG-18 says a `sql:` entry is
  accepted for run-scoped data-integrity invariants; it does not say whether the expression is a
  query whose returned **rows** are the violations, or one whose single computed **value** is
  compared against a bound. The two readings invert each other's verdict on the same SQL: read as
  failing-rows, `select count(*) from orders where total is null` fails on every run including the
  ones where the count is zero.

  This was invisible while nothing evaluated `sql:` at all — `internal/run` reports every one as
  unevaluated (R-VER-8), and an unevaluated assertion has no shape. It becomes a real fork the
  moment a tool can run them: soda-core has both shapes (`failed rows` + `fail query` versus a
  user-defined metric query with a bound), and `tortureu emit soda` (R-CLI-8) must pick one to
  emit an active check. It therefore emits **neither**, carrying every `sql:` assert into the
  generated checks file verbatim but commented out with both shapes written next to it, and the
  generated script refuses to scan while no check is active — because soda exits 0 on a checks
  file with no valid check, and a green result that checked nothing is worse than a red one.
  Resolves by amending R-CFG-18 to state the shape (or to require a per-entry discriminator).
- ~~**TBD-2**~~ — **RESOLVED**: `emit` prints to stdout by default (R-CLI-8), so its output composes with a shell redirect rather than requiring a path argument.
- **TBD-11** — How `tortureu` itself reaches a CI runner (R-CLI-11). There is no published
  release, tag, container image or marketplace action, so the generated pipeline builds the binary
  from the checked-out source (`go build ./cmd/tortureu`). That is correct inside this repo and
  wrong in a consumer repo, which has no TortureU source to build. The generated step is therefore
  marked in-file as the one line the user must adapt, rather than emitting an install command for a
  distribution channel that does not exist. Resolves when v0 ships an installable artefact.

- **TBD-13** — Whether the keploy handoff `capture -engine keploy` generates (R-CLI-12) actually
  records a session end to end. keploy 3.6.11's flags, its `keploy config --generate` output and
  its acceptance of a partial `keploy.yml` were verified against the real binary; the recording
  itself was not. The generated command *was* run against a real two-service compose stack: as an
  unprivileged user it stops in keploy's eBPF setup (`/proc/sys/kernel/perf_event_paranoid`
  permission denied); as root it gets past that, starts keploy's agent container beside the stack
  and creates a container named exactly the `container_name:` we derived `--container-name` from —
  then dies on Docker Desktop file sharing refusing keploy's own `./keploy` output mount, for every
  path tried. Both stops are this machine's configuration, not the generated command. Resolves when
  the handoff is exercised on a host with root and an unrestricted Docker bind-mount policy. Until
  then the generated command is stated as generated, never as run — which is exactly what
  `delegate` tier (R-SCOPE-3) promises and all it promises.

- **TBD-12** — How `--db-load` (R-EXE-26) and `--fuzz` (R-EXE-27) reach a SUT or database that
  **R-DC2-3** has put on an `internal: true` network. Both drive a real third-party binary as a
  subprocess, so neither can use the container-network-namespace join `internal/run`'s k6 path
  (`SetSUTContainer`, load.go) or its `fallbackTransport` (inreach.go) use — a subprocess dials
  from the host's own namespace. v0 therefore runs both as host processes against the address the
  caller supplied (`-db-url`, `target.base_url`), which covers a published port and a
  non-DC-2-isolated stack, and **fails loudly** (`status: error`) when the address is unreachable
  rather than reporting zero DB load or zero fuzz findings. Resolves by giving both runners the
  same `docker run --network container:<id>` mode `K6Runner` already has, which also needs a
  stated rule for translating the caller's address into that namespace — the part that must not be
  guessed, and the reason it is not done here.

- **TBD-5** — Whether to adopt the `grafana/k6-summary` JSON Schema once it leaves
  work-in-progress, replacing our own `handleSummary()` shape.
- ~~**TBD-6**~~ — **RESOLVED 2026-08-08: `"correlated"`.** The candidates were `"correlated"`, a
  distinct `"none"`, and the `""` the code actually shipped. The deciding evidence is what the run
  already does: `internal/run`'s `confidenceFor` derives a finding's confidence from the number of
  faults *TortureU itself scheduled* and from k6's own end-of-run summary — it never consults
  `Obs`. A repo with no Jaeger and no Prometheus still yields `correlated` findings today, because
  both halves of D-4's `correlated` row (a breach observed, exactly one fault active) come from
  *our* side of the wire: we own the independent variable, and k6 measures the response. The
  target's telemetry is what buys `caused`, and nothing else.

  So `"none"` would have been false — a ceiling the tool then routinely exceeds — and `""` was
  worse than false: it is the blank field that renders as nothing and JSON-omits itself
  (`max_confidence,omitempty` in both `internal/mcp` and `internal/verdict`), so `init` silently
  said nothing at all about confidence to exactly the repos that most needed telling. The field is
  a **ceiling, not a promise**: `correlated` says traces are what stand between this repo and
  `caused`; an individual finding still degrades to `ambiguous` under overlapping faults (R-VER-3).
  Consumer check: both consumers type the field as a plain string/`Confidence` and pass it through,
  and `"correlated"` is a value they already carry from the metrics-only path, so no consumer
  change is required.
- ~~**TBD-7**~~ — **RESOLVED 2026-08-08: implemented.** `Gemfile`/`Gemfile.lock` (Ruby) and
  `pom.xml` (Maven) now parse like the other manifests, so R-DET-14's v0 set is the whole of
  R-DET-1's list. Two details worth keeping. First, Ruby reads the **Gemfile's `gem` declarations**,
  falling back to the lockfile's `DEPENDENCIES` section — both are direct dependencies; the
  lockfile's `GEM specs:` block is the transitive closure, and attributing a gem Rails happened to
  pull in as a *client of this service* would be the guess D-3 forbids. Second, a Maven
  **aggregator** `pom.xml` (one declaring `<modules>`) is the honest gap case that survives: its
  real dependencies live in module `pom.xml`s outside any compose-declared directory, so reading
  the aggregator alone must report `platform:aws`/`azure`/`lacks:otel` as **undetermined**
  (R-COV-6) and name the unread modules as a gap — never "verified absent".
- **TBD-10** — **RESOLVED 2026-08-08.** Standard-library clients were invisible to D-9's candidate
  mechanism: candidates came from lockfile-detected clients (R-DET-5), and Go's `net/http` never
  appears in a `go.mod` require line, so no knob table could reach it. E1 measured the cost — the
  corpus's canonical "HTTP client with no timeout" case was detected and correctly attributed and
  still could not name `Client.Timeout` as the fix.

  Resolved along the line the requirements already drew: **R-DET-1 forbids *detection* reading
  source; R-AUD-5 permits the *audit* bounded inspection at known construction sites.** So
  `internal/doctor` gained an `http` entry and a fallback that fires only on real evidence of an
  `http.Client{` construction, and `internal/run` consumes audit findings alongside lockfile
  clients. Two details worth keeping: the audit searches for realistic **source forms**
  (`Timeout:`) rather than the qualified knob names it reports (`Client.Timeout` appears only in
  docs, never in source), and a stdlib-HTTP finding attaches to the **service whose source
  contains it**, naming its experiment target as undetermined rather than picking a dependency at
  random — an experiment pointing at the wrong host would teach the user something false.

- **TBD-9** — `Finding.Chain` (the fault -> symptom hop list in `VERDICT.md` §1) stays empty: no
  trace-ingestion pipeline exists in v0, and fabricating hops would be worse than omitting them.
  Binding once OpenTelemetry ingestion ships, which is also what raises confidence from
  `correlated` to `caused` (D-4). Raised by Task 7.
- ~~**TBD-8**~~ — **RESOLVED**: `capture` shipped **with** scrubbing in the same change, as the
  requirement demanded. R-CLI-9 now carries it, and the proof reads the written file back from disk
  rather than asserting on an in-memory struct.

---

## 13. How each requirement is verified

A requirement can be verified by a test, by an automated gate, by review, or not yet at all.
Conflating those is how a coverage percentage becomes a comfortable lie — so this section says
which is which, and the traceability report in `check.py` counts the first two only.

| Method | Meaning |
|---|---|
| **test** | a Go test cites the id (`// spec: R-XXX-n`) and fails if the behaviour regresses |
| **gate** | `check.py` fails the build if the requirement is violated |
| **review** | verified by the build record in `.superpowers/sdd/PLAN/progress.md`; not mechanically checkable |
| **benchmark** | verified by a committed measurement under `benchmarks/results/`, reproducible with `make bench` |
| **deferred** | not implemented; carries a `TBD-n` in §12 |

The requirements not verified by test or gate, and why:

| Requirement | Method | Why not a test |
|---|---|---|
| **R-PROC-1** (no code before a failing test) | review | A test cannot observe the order in which its own subject was written. Verified by the per-task reports, which record watching each test fail first. |
| **R-PROC-2** (spec before test) | review | Same: the artefact cannot witness its own history. Every task's escalations and the spec amendments answering them are the record. |
| **R-LIC-2** (generated scripts are inputs, not derivative works) | review | A legal position, not a program behaviour. What *is* gated: **R-LIC-1** (no AGPL import anywhere) and **R-LIC-6** (every driven tool's licence recorded). |
| **R-LIC-3** (redistributed k6 unmodified, with its licence) | deferred | We redistribute no k6 binary. Becomes testable if we ever do. |
| **R-LIC-4** (AGPL-3 §13 review before any hosted offering) | deferred | Conditioned on a hosted offering that does not exist. |
| **R-EXE-7** (platform support; WSL cgroup caveat) | review | Asserting macOS or WSL behaviour from a Linux CI runner would be a test that passes without evidence — the failure mode this project has rejected everywhere else. Needs real runners. |
| **R-SCOPE-1** (runs against compose, no Kubernetes) | test (indirect) | Proven by every Docker-backed test in `internal/run`: they bring up real compose stacks and no test anywhere requires a cluster. No single test names it because the whole suite is the evidence. |
| **R-DC1-4** (`init` notes division of labour when a k6 MCP is detected) | deferred | A **SHOULD**, and no file or format for k6 MCP registration is defined anywhere we could detect. Escalated during Task 8 and left unimplemented rather than guessed. |
| **R-EXE-24** (`jitter` is uniform, not Gaussian) | benchmark | Verified by measurement, not assertion: B1 measured a stddev of `27.95ms` for `jitter: 50ms` against the `28.9ms` uniform prediction (`j/√3`). A unit test could only re-assert the constant we chose; the measurement is what establishes Toxiproxy actually behaves this way. Committed under `benchmarks/results/`. |
| **R-DC2-5** (secret-scrub captured traffic on write) | deferred | **TBD-8**: capture does not exist in v0, so there is no write path to scrub. Must ship in the same change as capture, never after — scrubbing retrofitted onto an existing corpus means the unscrubbed cassettes already exist. |

Two requirements — **R-COV-7** and **R-DET-12** — are verified by tests added in the final coverage
pass; if they appear unverified in the report, the citation was lost and that is a defect.

---

## Traceability

`python3 check.py` reports which requirements have tests. It fails on a test citing a requirement
that does not exist here, and on any doc/registry disagreement.
