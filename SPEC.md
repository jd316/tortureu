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

**R-DET-17** *(proposed)* — The detected language **MUST** be resolved from **every** manifest
location R-DET-1 permits, not the compose-project root alone, in this order:

1. the compose-project root's manifest, if it has one (existing behaviour, unchanged);
2. otherwise the **SUT's own build-context manifest** — the SUT is the thing under test, so its
   language is the one that governs knobs, fixtures and emitted code (R-DET-8 identifies it);
3. otherwise the language of the other services' manifests, if they all agree;
4. otherwise **nothing**, plus a gap (R-DET-7) naming every language found.

The ordinary monorepo shape — `docker-compose.yml` at the root, each service's own `go.mod` under
its build context — reported no language at all, because only the root scan ever set it. That is not
a cosmetic gap: it renders a verdict candidate's `source` field empty, makes `emit fixtures` refuse
with "the language was not detected from your lockfile" and `emit testcontainers` refuse as non-Go,
and drops the manifest source from `doctor`'s knob attribution — for a repo whose language is
plainly readable from a file R-DET-1 already permits. Measured on the E1 corpus's own `case1`, whose
`api/go.mod` and `dep/go.mod` both exist and which reported `lang: null`.

Step 4 is a refusal, not a fallback: in a genuinely polyglot stack whose SUT has no manifest, a
wrong language means wrong knobs and an emitted fixture that does not compile, so the languages found
are named and none is chosen. A service with no manifest at all remains **not** a gap — R-DET-7's
existing rule, since a prebuilt binary or an ecosystem outside R-DET-1's list legitimately has none.

*(specified before the fix, per R-PROC-2; found cross-checking why a verdict candidate's `source`
field rendered empty)*

**R-DET-18** *(proposed)* — The **manifest-derived Coverage facts** (`platform:aws`,
`platform:azure`, and the client half of `lacks:otel` — R-COV-5) **MUST** be established from every
manifest R-DET-1 permits reading, on the same footing as R-DET-17's language: the compose-project
root's *and* each service's own build-context manifest. Each manifest **MUST** be matched against the
SDK table for **its own** language, not for whichever language `System.Lang` ended up holding.

Where a manifest was read but its declared dependencies were not (R-DET-14's aggregator-pom case),
each fact it would have decided **MUST** be `FactUnknown`, wherever that manifest was — a service
build context included. `FactFalse` is a positive claim of absence and **MUST** be reserved for
"every manifest R-DET-1 permits was read, and none of them declares it".

Before this requirement, only the root manifest could establish these facts, so the ordinary
monorepo — nothing at the root, `boto3` in `api/pyproject.toml` — reported `platform:aws` as
`FactFalse`: **verified absent**, when the truth was "we did not look there". That is the precise
distinction **R-COV-6** exists to keep, and consumers act on it differently — `emit localstack`
refuses with "no AWS SDK found" on `FactFalse` and with "the manifest could not be read" on
`FactUnknown`, and `doctor` treats `FactUnknown` as unevaluable rather than false. A wrong `FactFalse`
therefore does not merely lose information; it makes those refusals lie.

What does **not** change: a repo with **no** readable manifest anywhere keeps `FactFalse`, because
within R-DET-1's bound there is genuinely nothing that could declare an SDK — the honest reading of
"we looked everywhere we are allowed to look". And per **R-DET-10** these facts stay **manifest-only**
in both directions: a `localstack` compose image makes an `aws` *dependency* (§3.1) and **MUST NOT**
make `platform:aws` true, since a `dep:`-sourced signal is not a `lockfile`-sourced one.

*(specified before the fix, per R-PROC-2; found in the same sweep as R-DET-17 and confirmed by the
coordinator as the more serious half — an unhelpful empty string versus an unsupportable claim)*

**R-DET-19** *(proposed)* — Where **more than one** compose service declares `build:`, R-DET-8 does
not say which is the system under test, and the implementation picked whichever sorted last. Every
verdict, every fault target and every `base_url` hangs off that choice, so it **MUST** be made on
evidence and **MUST** be visible:

1. a service the caller names explicitly (`init -service <name>`) **MUST** win outright. A name that
   is not a service in the compose file **MUST** be an error naming the services that are — never a
   silent fall back to a derived pick.
2. with exactly one `build:` service, it is the SUT (R-DET-8, unchanged).
3. otherwise the candidates are the `build:` services that **no other service `depends_on`**. An
   application sits above its dependencies, so load enters the graph at the top. If exactly one such
   service exists it **MUST** be chosen, and `init` **MUST** mark it in the file it writes as derived
   rather than declared, naming `-service` as the override.
4. otherwise **no SUT is chosen**: `init` **MUST** omit `target.service` and report a gap naming
   every candidate, exactly as **R-CLI-19** does for an undecidable `base_url`. A guess here is
   worse than an absence — `run` refuses a config with no `target.service` in one line, whereas a
   wrong one silently tortures the wrong container and produces a verdict about it.

Measured over the nine real multi-`build:` stacks available (eight in `docker/awesome-compose` plus
`dockersamples/example-voting-app`): step 3 singles out exactly one candidate in **seven** of them,
and in every one it is the front door — `proxy` for `nginx-flask-mysql` and `nginx-aspnet-mysql`,
`nginx` for `nginx-nodejs-redis`, `frontend` for the three `react-*` stacks. Two are genuinely
ambiguous and reach step 4: `react-rust-postgres` (`backend` and `frontend`, neither depending on the
other) and `example-voting-app` (`result` and `worker`). The alphabetical pick got two of the nine
plainly wrong — `web2` rather than `nginx`, and, for `example-voting-app`, `worker`: a background
C# consumer that publishes no port at all, so `init` named as the system under test the one service
in that repo which cannot serve a request. No E1 corpus case has more than one `build:` service, so
none of them changes.

What does not change: a `build:` service that is not chosen is still neither a dependency nor a gap
(R-DET-2 and R-DET-8 classify by `build:`, not by being the SUT), and with a single `build:` service
nothing is reported at all — the overwhelming case stays silent.

*(specified before the fix, per R-PROC-2; found while verifying R-DET-16 against real repositories,
where the alphabetical pick is visible in the emitted `base_url`)*


**R-CLI-20** *(proposed)* — `doctor` **MUST** name the system under test it detected, **MUST**
report detection's gaps (**R-DET-7**), and **MUST** accept `-service` to name the system under test
when **R-DET-19** could not.

`doctor` is the verb whose entire purpose is telling a user what TortureU can and cannot determine
about their repo, and it referenced `System.Gaps` nowhere — so every gap detection produced was
invisible there: an unrecognised image, a manifest read but not followed, the languages found when
none could be chosen, and an undecided system under test. `init` printed all of them; `doctor`
printed none. The same fact reported by one verb and hidden by the other is worse than either
behaviour alone, because the user cannot tell which to trust.

Without `-service`, an ambiguous stack is also unresolvable at `doctor`: `init` lets the user say
which service is theirs, `doctor` gave them no way to, so the audit stayed empty with no stated
reason and no remedy.

*(specified before the fix, per R-PROC-2; found by running `doctor` against a stack with two
candidate `build:` services after R-DET-19 began refusing to guess)*

**R-CLI-21** *(proposed)* — `doctor` **MUST** state whether fault fidelity has been *measured* on
the platform it is running on, and **MUST NOT** imply it has where it has not.

B1 measures how closely an injected fault matches what was asked for, and every published B1 number
comes from **Linux with cgroup v2**. Fault delivery is platform-dependent — `netem`, cgroup CPU
quota and container pause all run in the Docker VM's kernel, and on macOS and Windows that is a
different kernel from the user's. A verdict there may be perfectly correct or quietly off, and
today nothing tells the reader which.

This is the project's own rule applied to itself: a number we have not measured is reported as
unmeasured, never inferred. A user on an unmeasured platform is told so once, plainly, with what it
does and does not affect — the orchestration, the egress topology and the attribution logic are the
same everywhere; only the *magnitude* of an injected fault is unverified.

*(specified before the fix, per R-PROC-2; raised because "macOS fidelity is unmeasured" was being
carried as a launch caveat in prose that no user would ever read)*

**R-VER-18** *(proposed)* — When a k6 summary's threshold entry is not the shape this project
parses, `run` **MUST** refuse and name the mismatch. It **MUST NOT** fall through to a default
verdict.

`internal/run` reads `metrics.<name>.thresholds.<expr>` as `{"ok": bool}`, which is what
`grafana/k6:0.54.0` — the pinned image — emits. **k6 v2 emits a bare bool, and inverts the
meaning**: measured against `grafana/k6:latest` (v2.1.0), a threshold that *passed* serialises as
`false` and one that *failed* as `true`, because the bool now reports "crossed", not "ok".

Today the type assertion simply fails, `ok` defaults to `false`, and **every assertion in the run
is reported as broken** — a verdict full of findings about a service that is fine. `K6Runner.Image`
is caller-configurable, so this is reachable now, not only after a future pin bump.

A shape we do not recognise is a tool error (**R-VER-2**, exit 2), never a result: reporting
"your p95 assertion failed" because we could not read the summary is the confident wrong answer
this project exists to avoid.

*(specified before the fix, per R-PROC-2; found while evaluating the k6 v2 bump TBD-5 describes)*

**R-VER-19** *(proposed)* — `run` **MUST** read both k6 threshold shapes and normalise them to one
meaning: *did this assertion hold?*

- k6 ≤ 1.x: `{"<expr>": {"ok": true}}` — `true` means **passed**.
- k6 ≥ 2.x: `{"<expr>": true}` — `true` means **crossed**, i.e. **failed**.

The two are exact opposites, so the shape determines the polarity and a reader that guesses is
wrong half the time. Normalisation happens once, at the parse boundary, and everything downstream
sees only "held" — no other code in this project may branch on a k6 version.

**R-VER-18's refusal stands for anything that is neither shape.** Widening what we accept must not
widen it to "whatever turns up": a third shape is still a tool error, because the failure this
guards against is not an unknown format, it is a *plausible* format read with inverted meaning.

*(specified before the fix, per R-PROC-2. Both polarities were measured against real containers —
`grafana/k6:0.54.0` and `grafana/k6:latest` v2.1.0 — not inferred from release notes)*
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

**R-DET-15** *(proposed)* — Where a caller does not name a compose file explicitly, TortureU
**MUST** resolve one using the Compose Specification's own documented precedence:

    compose.yaml  >  compose.yml  >  docker-compose.yaml  >  docker-compose.yml

and **MUST** report which file it chose, so a repo with more than one is never silently read from
the wrong one. When none exists, the error **MUST** name every filename that was looked for.

Every entry point that takes a compose path defaults this way — `init`, `run`, `doctor`, `check`,
`smoke`, `capture`, and the MCP tools — because a user meets whichever one they try first.

This requirement exists because the default was `docker-compose.yml` alone, and that is now the
*least* common spelling: of the 40 examples in Docker's own `docker/awesome-compose`, **37 use
`compose.yaml`, 2 use `compose.yml`, and none use `docker-compose.yml`**. `tortureu init` failed on
the first command against all three third-party repos it was tried on, before detection ran at all.
A tool whose very first step fails on the canonical filename of the format it targets is not
usable, whatever the rest of it does.

*(found cross-checking the growth strategy's time-to-first-verdict gate against real repositories
rather than against fixtures this project wrote; specified before the fix, per R-PROC-2)*

**R-DET-16** *(proposed)* — Detection **MUST** record the ports the system under test itself
**listens on** — the *container* side of a compose `ports:` entry (`target`), together with any
`expose:` entry — so that `init` can derive a `base_url` without guessing (**R-CLI-19**).

It is the container side, not the published host side, because of where the load is dialled from:
`run` executes k6 inside the SUT container's own network namespace (`--network container:<id>`, the
**R-DC2-3** fix in `internal/run/load.go`), and `--fuzz` joins the identical container
(**R-EXE-27**). Inside that namespace `localhost:<container port>` is the SUT's own loopback, and the
published host port is not bound at all. Every other consumer of `target.base_url` reads its port
the same way: `emit netem/iptables` filters the SUT's own `INPUT --dport` with it, and
`emit kind` maps it as a `containerPort`. A `base_url` built from the *host* side would therefore be
wrong for exactly the mappings where the two differ — E1's own control case publishes `8081:8080`,
so the host reading gives `localhost:8081`, an address nothing in that namespace answers on.

Two entries **MUST NOT** be recorded, neither of which names a reachable TCP port a base URL could
use: a `target` of `0`, and a non-TCP (`udp`) port. A port *range* needs no special rule: compose-go
expands `8000-8002` into its individual members, each of which is a genuine candidate, and
**R-CLI-19**'s several-ports refusal then names them all — compose does not say which member serves
the API, so that is the honest outcome rather than a special case.

Duplicates **MUST** collapse: `ports: ["8080:8080"]` alongside `expose: ["8080"]` is one port, not
two, and counting it twice would send **R-CLI-19** down its several-candidates refusal for a service
with a single obvious answer.

*(specified before the fix, per R-PROC-2; resolves the detection half of **TBD-15**. The
container-side reading was found by cross-checking the emitted `base_url` against the E1 corpus's
committed configs: `case8-control`'s says `8080` with a comment explaining exactly this, and it is
right)*

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

**R-CFG-18** — A `sql:` entry **MUST** be accepted for run-scoped data-integrity invariants, and its
expression **MUST** be a **violation count**: a query returning **exactly one row and exactly one
column, holding a non-negative number** — how many rows violate the invariant. The invariant holds
if and only if that number is `0`. *(resolves TBD-14)*

- Any other result — more than one row, more than one column, `NULL`, a value that is not a number,
  or a negative number — **MUST** be a **tool error** (**R-VER-2** `error`), never a pass and never
  a fail. A rows-shaped query (`select * from orders where total is null`) is therefore *refused
  with its shape named*, not silently reinterpreted; the rows shape is written
  `select count(*) from (<rows query>) t`.
- The database connection **MUST** be supplied explicitly. TortureU **MUST NOT** infer, default or
  guess a host, user, password or database name, and **MUST** refuse with a message naming what is
  missing.
- With no connection supplied, every `sql:` entry **MUST** be reported unevaluated (**R-VER-8**) —
  never as a held assertion (**R-VER-5**).
- A database that cannot be reached, or a query the engine rejects, **MUST** be a tool error
  (**R-VER-2**), never a passing invariant. A violated invariant is a **result** (`fail`).
- A held `sql:` assertion **MUST** report its observed violation count (`0`) and a broken one the
  count it actually measured, so the verdict carries a measured value rather than a restatement of
  pass/fail (**R-VER-5**).

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
- **no binary**: `pgbench` absent from `PATH` **and** no `docker` to run its official image (see
  *Reach* below) **MUST** be reported with an install hint in the manner of **R-CLI-5**, naming
  both routes, never as an obscure failure;
- every refusal above **MUST** happen *before* reset and before any load starts. Discovering the
  flag was unusable after a run has already perturbed the stack teaches the user nothing.

`pgbench`'s own initialization (`pgbench -i`) **creates and drops tables named `pgbench_*`** in the
target database. That is a write against the caller's data, so the flag's help text **MUST** say
so; it is not something a user may discover from the effects.

Reach: the database `-db-url` names may sit on the internal-only network **R-DC2-3** creates, for
which Docker publishes no host port at all — so a host-process pgbench cannot dial it. `--db-load`
**MUST** therefore reach it the way the load path already does (**R-CLI-6**'s rule): a direct dial
from the host first, falling back to running pgbench's own official image in a container that joins
the *database container's* network namespace (`docker run --network container:<id>`). The address
translation **MUST** be stated rather than guessed, and this is the statement:

- the container joined is the one Docker reports for the compose service `-db-url`'s **own host**
  names — the caller's address is what selects it, so nothing is inferred from the compose file;
- inside that namespace the host becomes `127.0.0.1` and **the port stays exactly what `-db-url`
  already says**. This is not a choice between candidates: a container with no published port can
  only be named by the port its own server listens on, so the caller's port *is* the in-namespace
  port or the caller's address was never valid.

A `-db-url` whose host names no running compose service, or whose form this rule cannot rewrite,
**MUST** keep failing loudly (**R-VER-2**'s `error`) rather than being guessed at.

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
- **no binary**: `schemathesis` (or its `st` alias) absent from `PATH` **and** no `docker` to run
  its official image (see *Reach* below) **MUST** carry an install hint per **R-CLI-5**, naming
  both routes;
- all three **MUST** be checked before reset and before load.

Reach, in the same shape as **R-EXE-26** and with strictly less to state: a SUT **R-DC2-3** put on
an internal-only network publishes no port, so `--fuzz` **MUST** likewise dial `target.base_url`
directly first and fall back to running schemathesis's own official image in a container joining
the **same** container the load generator joins — `target.service`, resolved exactly as the load
path resolves it. There is no address to translate at all: `target.base_url` is fuzzed
**unchanged**, because it is the identical address k6 itself uses from inside that identical
namespace. A fuzz pass and the load it runs under therefore cannot disagree about what they were
pointed at.

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

**R-VER-12** — The verdict **MUST** carry the commit the run was made against, in `VERDICT.md`
§1's `commit` field, resolved from the git `HEAD` of the repository containing the target's compose
file. It **MUST** be the **full 40-character hash** in the JSON document: `VERDICT.md` §4's human
rendering abbreviates it for display, but a store that anchors trends on an abbreviated hash can
collide, and at least one real consumer (`tortureu emit bencher` → `bencher run --hash`) rejects
anything shorter outright.

When the repo is not a git checkout, or git is unavailable, or `HEAD` cannot be resolved, the field
**MUST** be left empty rather than filled with a placeholder. An invented anchor is worse than an
absent one: a trend line silently keyed on a fabricated commit compares runs that are not what it
says they are.

*(the field existed in `VERDICT.md` §1 and in `internal/verdict` and was written by no producer
anywhere in the codebase, so every verdict emitted an empty anchor; found while implementing
`emit bencher`, which is the consumer that needs it. Specified here before the fix, per R-PROC-2)*

**R-VER-13** *(proposed)* — A finding's `chain` (`VERDICT.md` §1's fault → symptom hop list)
**MUST** be derived from real ingested spans, one hop per span on a real request path, or stay
empty. It **MUST NOT** be synthesized from the fault declaration, from detection, or from any
other source that does not observe the request path: an invented causal story is worse than an
absent one, because it is the one field a reader cannot check.

Ingestion is by **query against a trace backend**, not by re-detection: whether this repo has
tracing at all is already **R-DET-12**'s and **R-COV-6**'s answer (`Obs.Traces`,
`Coverage.LacksOtel`), and ingestion **MUST** consume those facts rather than recompute them.
The backend supported in v0 is **Jaeger's query API** (`GET /api/services`,
`GET /api/traces?service=&start=&end=&limit=`, microsecond epoch bounds) — one backend queried
correctly beats three approximated. A reachable endpoint that is **not** Jaeger **MUST** be
refused by name where it is identifiable (Tempo answers `GET /api/echo` with `echo` and has no
`/api/services`; the OpenTelemetry Collector is a pipeline with **no** query API at all, so a
collector in compose is not by itself an ingestible source) and the chain stays empty. A refusal
**MUST** say which backend was found and that only Jaeger is implemented — never fall through to
silence.

The chain is derived as follows, and every value in it is measured:

- The hop path is the **parent chain of a real span matching the fault's target** (`cause.target`,
  `host:port`) up to its trace root. A span matches the target when its service name is the
  target's host, or when its own attributes name that peer (`net.peer.name` / `net.peer.ip` /
  `server.address` / `peer.service`, with `net.peer.port` / `server.port` when the tag is present).
- Each hop's `observed` is that hop's own **measured latency change** across every sampled span
  with the same service and operation: baseline is the median of the fastest quartile, degraded is
  the p95, and the span count is reported so a reader can weigh it.
- If the target hop shows **no degradation**, the chain **MUST** stay empty. Traces existing is not
  evidence that the fault reached the request path. Degradation is **both** a ratio and an absolute
  step: p95 at least **twice** the baseline **and** at least **10ms above** it. The ratio alone is
  not enough at sub-millisecond span durations, where ordinary scheduler and timer jitter clears it
  routinely — measured on E1's case 9, where an *undisturbed* dependency's 587µs baseline against a
  1.3ms p95 read as "2.2x degraded" while the genuinely faulted dependency next to it went from
  817µs to 3002ms. A step that small cannot explain an SLO breach measured in hundreds of
  milliseconds, and counting it as evidence is what turns a measurement into a coin toss —
  under **R-VER-17** it is what makes one real cause and one noisy neighbour look like two
  candidates.

An **explicitly configured** endpoint **MUST** be queried through the same reach-into-the-stack
transport the orchestrator's other outbound calls use (**R-DC2-3**: a direct call first, falling
back to a tunnel through the target container's own network namespace). A trace backend that is
part of the system under test's own compose stack has, under DC-2 enforcement, **no published host
port** — measured on E1's case 9, where the topology overlay left the stack's Jaeger reachable only
from inside the internal network, so a plain host-process HTTP client could never read a single
span and every finding stayed unattributed. The guessed `localhost` endpoint is exempt: nobody
named it, so it stays a plain, cheap probe.

The chain **MUST** stay empty — exactly as when no ingestion exists — when traces are absent,
the backend is unreachable or unsupported, no span matches the fault's target, no degradation is
observed at it, or no single fault identifies a target at all.

**R-VER-14** *(proposed)* — A finding's confidence **MUST** be raised to `caused` **only** when

**R-VER-15** *(proposed)* — A metric value rendered into a verdict — `Broke.Observed`,
`Passed.Observed` — **MUST** be formatted for a reader, in both the JSON document and the human
rendering (**R-VER-9**: one document, two renderings): carrying its unit where the unit is known,
and rounded to a readable precision rather than a float's full decimal expansion.

`VERDICT.md` §1 and §4 both specify this shape (`"observed": "4218ms"`). The code emitted
`3003.2139021999997`, because the only unit rule it had keyed off a `contains: "time"` field that
**real k6 `--summary-export` output does not contain** — the branch could never fire against the
actual tool. The unit is therefore taken from the metric name, which k6 does define: its
`*_duration` trend metrics are milliseconds.

Precision **MUST** be significant-figure based, not fixed-decimal: a rate of `0.003` and a duration
of `3003.21` appear in the same document, and rounding both to two decimal places would erase the
first.

Where the unit is genuinely unknown, the value **MUST** be rendered bare rather than given a
guessed one — a wrong unit is a wrong measurement, and this document is read as evidence.

*(specified before the fix, per R-PROC-2; the defect was found cross-checking emitted output
against `VERDICT.md` §1 and §4)*

**R-VER-16** *(proposed)* — A verdict with `status: aborted` **MUST** carry the reason it aborted in
its `error` field, and the reason **MUST** include the underlying tool's own output where there is
any. `reset: failed` alone is not a reason.

The reset command is a shell command TortureU runs on the user's behalf (**R-CFG-21**), so its
failure is almost always something only its output explains — a missing secret file, a port already
bound, an image that will not pull. `ShellResetter` used `cmd.Run()`, which discards stdout and
stderr, and `internal/run` discarded the returned error as well, so a real first-run abort rendered
as `reset: failed` with `"error": null` and nothing else anywhere.

Measured: `tortureu run` on `docker/awesome-compose`'s `nginx-golang-postgres` aborts because that
example needs a `db/password.txt` the repo does not ship. Docker says exactly that. TortureU said
`reset: failed`. Aborting was correct; being undiagnosable was not — and this is a user's first
run, which is where an unexplained failure costs most.

*(specified before the fix, per R-PROC-2; found by measuring the growth strategy's
time-to-first-verdict gate on repositories this project did not write)*

**R-EXE-28** *(proposed)* — `run` **MUST** refuse, before reset and before any load, when
`target.base_url` is empty. It **MUST NOT** start load against it.

k6 given no base URL requests `"/"` with no scheme and every single request fails
(`unsupported protocol scheme ""`). The run then completes and produces a verdict whose
`http_req_failed` rate is 1.0 — a document that looks like a finding about the user's service and
is entirely an artefact of a missing config field. That is the worst failure this project has: not
a crash, but a confident wrong answer.

Measured on `docker/awesome-compose`'s `flask-redis`: `init` wrote no `base_url`, `run` fired a
full load of failing requests, and returned exit 4 with a wall of k6 warnings. The refusal must
name the field and say it is not detected, per **R-CLI-4**'s rule against writing an empty required
field.

*(specified before the fix, per R-PROC-2; found measuring time-to-first-verdict on a third-party
repository)*
**R-VER-13**'s chain was actually built for that finding, and **MUST** then be clamped to the
ceiling `observability.max_confidence` already carries (**R-DET-6**, TBD-6): a finding may never
claim more than the ceiling states. With no chain, confidence is unchanged from what
**R-VER-3**'s fault-count rule gives — `correlated` for a single fault, `ambiguous` otherwise.
This is the mechanism D-4 names: the fault schedule and the load generator are enough for
`correlated` because we own the independent variable; only the target's own telemetry can show
the request path through the degraded dependency, which is what `caused` asserts.

*(both proposed by the implementer and specified before citation, per R-PROC-2; they resolve
TBD-9, and R-VER-3's `caused` row had no producer anywhere in the codebase before them)*

**R-VER-17** *(proposed)* — When **two or more** faults were active, a finding's `cause` **MUST**
stay unset unless real ingested spans show that **exactly one** of those faults' targets degraded.
When they do, `cause` **MUST** be set to that one fault and the chain built from its target per
**R-VER-13**.

"Degraded" is **R-VER-13**'s own measured gate applied per candidate target, and nothing weaker: a
target counts as degraded exactly when **R-VER-13**'s derivation produces a non-empty chain for it
— a real span matching that `host:port`, whose p95 clears **both** halves of **R-VER-13**'s gate
(twice its own fastest-quartile baseline, and at least 10ms above it). There is deliberately no
second, looser definition of degradation for this rule.

The candidate set is the active faults' non-empty `host:port` targets. A fault whose target is not
an address (a queue fault's topic name) is not a candidate and **MUST NOT** be attributed this way,
because no span attribute names it.

Attribution **MUST** be refused — the finding staying `ambiguous` with `cause` unset and `chain`
empty, exactly as **R-VER-3**'s fault-count rule leaves it — when **any** of these hold:

- two or more candidate targets degraded (two degraded dependencies is genuinely ambiguous, and
  naming one of them would be a guess dressed as a measurement);
- no candidate target degraded;
- trace data is absent, the backend is unreachable, unsupported or unreadable, or it returned no
  traces;
- two or more of the active faults share the single degraded target — degradation cannot
  distinguish between them.

Confidence then follows **R-VER-14** unchanged: a chain was actually built, so the finding earns
`caused`, clamped to the `observability.max_confidence` ceiling. A finding attributed under this
rule but carrying **no** chain **MUST NOT** be produced: the attribution and the chain are read off
the same spans, so either both exist or neither does.

*(proposed by the implementer and specified before citation, per R-PROC-2. Measured gap: **R-VER-3**'s
fault-count rule collapses every multi-fault finding to `ambiguous` with no cause, and **R-VER-13**'s
chain builder returned early unless a cause was *already* set — so ingested traces could raise the
confidence of an attributed finding but could never attribute one. That is precisely the case D-4
reserves traces for: "overlapping faults, no traces" is `ambiguous` **because** there are no traces,
and this rule says what changes when there are.)*

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

The reason is concrete. k6 0.54.0's `--summary-export` reported **every threshold as `false` on any
arrival-rate executor regardless of the measured value**, and **R-CFG-6 permits only** arrival-rate,
so the single executor family we may use is the one where its export is unusable. Worse,
`--summary-export`'s writer recomputes independently and **silently discards** whatever
`handleSummary()` returned — verified by overwriting `ok:true` before returning and observing no
effect on disk. Depending on any of k6's booleans therefore makes our verdict hostage to a
behaviour that varies by executor and by version, without a signal when it changes.

E1 measured the cost of not doing this: a control backend with **no defect at all** produced two
findings in three of four runs. *(E1 → Task 4, 2026-08-08)*

> Grafana also publishes a JSON Schema for the end-of-test summary (`grafana/k6-summary`),
> intended as the stable automation contract. It has since shipped as `1.0.0` and is no longer
> work-in-progress, but k6 emits it only behind `--new-machine-readable-summary` (opt-in even in
> v2.1.0). The pin is now `grafana/k6:2.1.0` (TBD-5), so the shape is available — but it stays
> opt-in, and R-VER-10 recomputes every verdict from measured values anyway, so v0 still targets
> `handleSummary()` and adopting the richer document has no forcing reason. *(TBD-5)*

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
| `trend` | record a verdict locally → cross-commit trend |

**R-CLI-2** — Every verb **MUST** be listed in `registry.yaml` as the `how:` of at least one tool.

**R-CLI-14** *(proposed)* — `trend` **MUST** provide two modes over a local store of verdicts:
`trend record <verdict.json>` appends one record for one verdict document, and `trend show` prints
the series accumulated so far. `record` **MUST** accept `-` as stdin, so `tortureu run -json |
tortureu trend record -` needs no intermediate file — the same composability reason `emit` prints to
stdout (**R-CLI-8**).

`show` **MUST** answer the two questions a cross-commit trend exists to answer, and **MUST** answer
both:

1. **did a number move** — for every numeric metric the store carries, its value at each recorded run
   and the delta against the previous recorded run of the same scenario;
2. **did a finding appear that was not there before** — per run, the findings that are new against
   the previous run and the findings that were present before and are now gone.

A finding's identity for that comparison **MUST** be its assertion text plus the fault named as its
cause, **never** the verdict's `id` field. `f1` is a position in one run's ordered list (`VERDICT.md`
§1 orders findings worst-first), so comparing `id`s across runs reports a change whenever the
ordering changed and reports none when the first finding was replaced by a different one.

`show` **MUST NOT** fail the process because a metric regressed. It exits `0` whenever the store
could be read and `2` when it could not. Choosing the boundary at which a slower p99 becomes a red
build is a threshold policy, and `torture.yaml` states none — the same reason `emit bencher` writes
its `--threshold-*` flags as commented examples rather than picking one.

**R-CLI-15** *(proposed)* — The store **MUST** be **JSONL**: one JSON object per line, append-only,
at `.tortureu/trend.jsonl` by default and overridable with `-store`. Each record **MUST** carry a
schema version. A record whose version this build does not understand, and a line that does not
parse, **MUST** each be reported with their line number and skipped — never guessed at, and never
silently dropped (**R-COV-6**). One corrupt tail line **MUST NOT** cost the reader the history above
it.

A record **MUST** be a *projection* of the verdict, not the verdict document itself: the run's
identity (`run_id`, `scenario`, `started_at`, `duration_s`), its anchor (`commit`), its outcome
(`status`, exit code), the numeric leaves of `metrics` flattened to dotted keys, and the finding
keys **R-CLI-14** compares. Storing whole verdicts would make the file unreadable in a `git diff`
and unbounded in size, and neither buys anything a trend joins on. Everything dropped is
reconstructible from the verdict document the run already emitted; nothing dropped is *derivable*
from the projection and then silently wrong.

**R-CLI-16** *(proposed)* — `record` **MUST** be safe against concurrent writers — two CI jobs
finishing at the same moment, on the same store. It **MUST** hold an advisory exclusive lock across
a single append of one whole line, so the outcome is two complete records in some order, never one
interleaved line that parses as neither run. It **MUST NOT** rewrite bytes it did not just append:
a writer that can also modify history turns a torn write into unrecoverable data loss instead of one
skippable line.

**R-CLI-17** *(proposed)* — A verdict whose `commit` is empty — **R-VER-12**'s honest answer for a
run made outside a git checkout — **MUST** still be recorded, **MUST NOT** enter the series, and
**MUST** be reported by `show` as excluded, with the count and the reason. The two obvious
alternatives are both worse: refusing to record loses a real run because of where it was run, and
letting it join keyed on `""` collapses every anchorless run onto a single point and silently
corrupts the trend those points sit in.

The same distinction applies to outcome. A record whose status is `error` or `aborted` carries no
measurement of the system under test (**R-VER-2**), so its metrics **MUST NOT** enter any delta;
its row **MUST** still be shown, with its status, so the reader sees a gap in the series rather than
a continuity that was never measured.

*(all four proposed by the implementer and specified before citation, per R-PROC-2; they resolve
TBD-1)*


**R-CLI-18** *(proposed)* — `run` **MUST** accept `--trend`, which appends the run's verdict to the
**R-CLI-15** store as if `trend record` had been piped it. Without it, recording a trend requires
remembering `tortureu run -json | tortureu trend record -`, and a trend nobody remembers to record
is not a trend.

It **MUST** default to **off**: a verb that writes a file into the user's repo without being asked
is a surprise, and `run` is the verb most likely to be run casually.

Appending **MUST NOT** change the run's exit code or its rendered verdict. A store that cannot be
written is reported on stderr and nothing else — **R-VER-7**'s codes describe what the *experiment*
found, and letting a bookkeeping failure overwrite that would report a resilience result the run
did not reach.

*(the store and both `trend` modes shipped first (R-CLI-14..17), reachable only through a shell
pipeline; specified here before wiring it into `run`, per R-PROC-2)*
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

The step that installs `tortureu` **MUST NOT** build it from the checked-out source. A consumer's
repo contains no TortureU source, so `go build ./cmd/tortureu` describes a pipeline that can only
run inside this repo — a generated file that is wrong everywhere it is meant to be used. The
install step **MUST** instead take a **published, version-pinned artefact**: a release archive
whose checksum is verified against the release's own `checksums.txt`, or a container image at the
same pinned tag. It **MUST NOT** float on `latest` or `@latest`, in either form — a pipeline whose
binary changes under it cannot tell a regression in the service from a change in the harness.

While no such artefact is published, the generated install step **MUST** fail the job with exit `2`
(harness error, **R-VER-7**), state plainly that no release exists yet, and list the routes the
reader may substitute. Two things it **MUST NOT** do: emit a download of a URL that does not
resolve, which reports a missing release as a network accident; and let the job continue, which
reports it as nothing at all. The same prohibition on `continue-on-error` / `allow_failure` applies
here as to the run step — an uninstallable harness is exit `2`'s literal meaning, not a warning.
*(resolves TBD-11 as far as it can be resolved before a maintainer pushes the first tag: the
mechanics, the pinning rule and the pre-release behaviour are fixed here; the pinned version itself
is one constant, set when the tag exists)*

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
registry entry named `--engine` with nothing behind it — see TBD-13 for the end-to-end run that
confirmed the generated command records and replays)*

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

**R-CLI-5** — `doctor` **MUST** report whether the tools a run needs are present on this machine,
and `init` **MUST** warn about any that are missing without failing — writing a config is still
useful on a machine that cannot yet run.

Presence is what can be checked, so presence is what is claimed: a found binary is reported as
found, never as working. Missing entries **MUST** carry an install hint.

**What `run` actually needs is `docker` and `docker compose` — not `k6` on `PATH`.** This
requirement used to name k6 among them, from when a host-process k6 was the default. **R-DC2-3**'s
`internal: true` topology ended that: Docker publishes no host port for a container whose only
network is internal, so `run` always executes k6 in the pinned `grafana/k6` image sharing the SUT's
network namespace, and the host-process path is never taken. A machine with no k6 runs the whole
suite and the whole eval corpus today.

So k6 **MUST NOT** be reported as a missing prerequisite. It **MAY** be reported as absent-and-not-
required, and the report **MUST** say the container is used. Telling a new user to install a tool
the tool does not use inverts the failure this requirement exists to prevent: instead of a late
failure after two steps of setup, it is an unnecessary chore before step one — and for an audience
whose stated barrier is fear of breaking things, the first instruction being wrong is worse than
late.

*(behaviour shipped in Task 8 and specified after the fact; the k6 correction came from a
cross-check of the growth strategy's time-to-first-verdict gate, which is exactly the path this
friction sits on)*

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

**R-CLI-19** *(proposed)* — `init` **MUST** write `target.base_url` when, and only when, the SUT
declares **exactly one** listening port (**R-DET-16**), as `http://localhost:<port>`. In every other
case it **MUST NOT** write a value, and **MUST** instead say in the file what it found and why it
refused, and report the same as a gap (**R-DET-7**):

| Ports the SUT declares | What `init` writes |
|---|---|
| exactly one | `base_url: http://localhost:<port>`, with the assumptions named |
| several | no value — a comment naming **every** candidate URL, for the user to choose between |
| none | no value — a comment saying compose declares no port for this service |

`localhost` is correct rather than the compose service name because of where the dial happens
(**R-DET-16**): k6 runs inside the SUT's own network namespace, so the SUT's loopback is the SUT.
That it *binds* loopback (rather than only its container IP) is an assumption, and the same one E1's
own configs already make.

The scheme is also an assumption and **MUST** be labelled as one. `http` is the defensible default —
a container that terminates TLS itself is the rare case, and compose gives no evidence either way —
but a port number is **not** evidence: `443` or `8443` **MUST NOT** be read as `https`, because a
scheme guessed from a convention is exactly the confident-wrong-answer failure **R-EXE-28** exists
to stop.

Several ports **MUST NOT** be resolved by picking one, by any rule (lowest, first, "the
HTTP-looking one"). A `base_url` naming a debugger or an admin port looks detected, runs, and
measures the wrong thing — strictly worse than the empty field, which **R-EXE-28** turns into a loud
refusal naming the field. The measured case is `immich`'s dev compose, whose SUT declares `3000` and
`24678`: the second is Vite's HMR socket, and load pointed at it would measure the dev server's
file-watcher rather than the application.

*(specified before the fix, per R-PROC-2; resolves the `init` half of **TBD-15**)*

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

- ~~**TBD-1**~~ — **RESOLVED 2026-08-09: JSONL, at `.tortureu/trend.jsonl`, read by
  `tortureu trend` (R-CLI-14..17).** Verdict storage format for cross-commit trend tracking
  (SQLite / JSONL / Bencher-compatible). The question was narrowed twice before it was answered,
  and both narrowings are worth keeping because they are what made the remaining choice small.

  **The Bencher-compatible option was never a third storage format.** `tortureu emit bencher`
  (R-CLI-8) is built and verified against the real CLI (bencher 0.6.11): Bencher Metric Format is a
  *projection* of one verdict computed at emit time, and the history lives on Bencher's server.
  Nothing is stored on our side, so that option answers "where does a repo *with* a Bencher project
  keep its trend" and says nothing about a repo without one. Those two remain complementary rather
  than competing: the same projection idea, one pointed at a server, one at a file.

  **The binding constraint was the anchor, not the format.** Every trend joins on a per-run commit,
  and `verdict.Commit` — VERDICT.md §1's `commit` field, the one it labels "for §12 trend tracking"
  — was written by no producer in this codebase, so any format chosen would have been a store of
  unanchored rows. That prerequisite is now closed by **R-VER-12**: `internal/run` resolves the full
  40-character hash from the git HEAD of the repo containing the compose file, and leaves it empty
  outside a checkout rather than inventing one. (Bencher's `--hash` rejects anything shorter — even
  VERDICT.md's own example value `a3f19c2`.)

  **Why JSONL and not SQLite**, given the store now has something real to hold:

  - **It is the artefact that survives being committed.** A trend is only worth keeping if it
    outlives the machine that produced it, which means it goes in the repo. A SQLite file is opaque
    to `git diff`, rewrites pages on every insert so every run is a whole-file binary churn, and
    conflicts unmergeably when two branches each record a run. An append-only JSONL file diffs as
    the one line that was added, and two branches that each appended merge by concatenation. A repo
    that would rather not track it gitignores one path either way, so JSONL is strictly better on
    the axis that actually differs.
  - **The dependency cost is real and the query benefit is not.** SQLite in Go is either cgo
    (`mattn/go-sqlite3`, which ends the single static binary D-6 exists for) or a very large pure-Go
    transpilation (`modernc.org/sqlite`). `go.mod` currently has two direct dependencies. What that
    buys is indexed query over a table whose row count is *one per run* — a few thousand rows after
    years of CI. Reading the whole file and grouping in memory is not the slow path, and will not
    become one.
  - **Concurrent writers do not need a database.** The one genuinely hard requirement — two parallel
    CI jobs recording at the same instant — is satisfied by an advisory exclusive lock held across a
    single append of one whole line (R-CLI-16). SQLite would solve the same problem with a
    write-ahead log, a lock file and a `database is locked` failure mode. The failure mode here is
    one skippable malformed line, and R-CLI-15 requires the reader to skip it by line number rather
    than lose the history above it.
  - **A verdict is already JSON.** The store is a projection of the document `run` emits, in the
    same encoding, so there is no schema migration step between "we have a verdict" and "we have a
    row" — which is also why the projection is explicit (R-CLI-15) rather than the whole document:
    the file stays readable and bounded.

  What the resolution deliberately does **not** do: fail a build on a regression (R-CLI-14 — that is
  a threshold policy nothing has stated), and enter an anchorless or unmeasured run into the series
  (R-CLI-17 — a row with no anchor is kept and shown as excluded, never joined on `""`).

- ~~**TBD-14**~~ — **RESOLVED 2026-08-09: a `sql:` expression is a VIOLATION COUNT.** The question
  was what SHAPE a `sql:` assertion (R-CFG-18) is: a query whose returned **rows** are the
  violations, or one whose single computed **value** is compared against a bound. The two readings
  invert each other's verdict on the same SQL — read as failing-rows,
  `select count(*) from orders where total is null` fails on every run including the ones where the
  count is zero. R-CFG-18 now states the shape: one row, one column, a non-negative number, and the
  invariant holds iff it is `0`. Anything else is a tool error.

  **Why the count and not the predicate.** The polarity that would have matched `promql:` (R-CFG-17,
  where the user writes the condition they want to *hold*) is `select count(*) = 0 from ...` — a
  boolean. That reading cannot be made safe across the two engines this project supports, because
  **MySQL has no boolean type**: `count(*) = 0` comes back as `1`/`0`, indistinguishable from a
  count. Under the predicate reading, a user who writes the plain count query on MySQL and has
  three violations gets back `3`, which is truthy, and the invariant reads **pass** — a green that
  means the opposite of the truth, the single failure mode R-VER-8 exists to prevent. Under the
  violation-count reading the mirrored mistake (`count(*) = 0` on MySQL, returning `1`) reads as
  *one violation* and **fails**. Only one of the two readings has a fail-safe worst case, and that
  decides it; the polarity difference against `promql:` costs a sentence of documentation, which is
  cheaper than a false green. What the two escape hatches *do* keep identical is their lifecycle:
  unevaluated when no endpoint is configured, a real measured value on the verdict, and a backend
  that cannot be reached is a tool error rather than a verdict.

  **The rows shape is not lost, and it is not ambiguous.** Any rows query becomes
  `select count(*) from (<rows query>) t`, so nothing expressible before is inexpressible now. A
  rows-shaped query written directly returns many rows and/or many columns, which R-CFG-18 makes an
  error naming the shape it got — so the failing-rows reading cannot be *silently* taken. A user
  cannot write an ambiguous `sql:` entry: every query either is the one legal shape or is refused.

  **Why not a per-entry discriminator supporting both shapes.** A discriminator adds a second place
  the shape can be wrong — the config claims `rows:` while the SQL computes a count, and nothing can
  detect the mismatch, because a one-row one-column result is a perfectly good "one violating row".
  One shape, checked against the result the engine actually returns, makes the wrong shape
  unrepresentable instead of merely discouraged.

  **Downstream, this unblocks `tortureu emit soda`** (R-CLI-8), which emitted every `sql:` assert
  commented out with both shapes written next to it precisely because of this TBD. Soda's
  user-defined-metric check (`<metric> = 0` plus a `<metric> query:`) requires exactly one row and
  one column — the same shape R-CFG-18 now mandates — so the emitter now emits an **active** check
  per assert, in that shape, and its refuse-to-scan-with-no-active-check guard is left in place for
  the case where `torture.yaml` declares no `sql:` assert at all.
- ~~**TBD-2**~~ — **RESOLVED**: `emit` prints to stdout by default (R-CLI-8), so its output composes with a shell redirect rather than requiring a path argument.
- ~~**TBD-11**~~ — **RESOLVED 2026-08-09, except for one constant that only a tag can set.** How
  `tortureu` itself reaches a CI runner (R-CLI-11). The old answer — build it from the checked-out
  source — was correct inside this repo and wrong in every repo the generated file is *for*.

  The release mechanics now exist rather than being wished for: `.goreleaser.yaml` builds
  `linux`/`darwin` × `amd64`/`arm64` static binaries with a `checksums.txt`, `Dockerfile` builds a
  CI-shaped image (`tortureu` on top of `docker:cli`, so the container that runs the experiment can
  reach the compose stack), and `.github/workflows/release.yml` runs both on a `v*` tag. Three
  install routes therefore exist, and R-CLI-11 now names which one the generated pipeline takes:
  GitHub Actions downloads the pinned release archive and verifies it against `checksums.txt`;
  GitLab runs the job *in* the image at the same pinned tag; and `go install
  github.com/jd316/tortureu/cmd/tortureu@<tag>` is the zero-infrastructure route documented in
  README for humans, deliberately not the one CI takes — it needs a Go toolchain on the runner and
  pins nothing that a checksum could verify.

  The pinning rule is the part worth keeping: **no `latest`, in any spelling.** A harness that
  updates itself under a pipeline makes every regression ambiguous — the service changed, or the
  thing measuring it did, and the run cannot say which.

  **Closed 2026-08-29:** `v0.1.2` is tagged and published — cross-platform archives with
  `checksums.txt`, and `ghcr.io/jd316/tortureu:v0.1.2`. `ci.ReleaseVersion` is set, so the pipeline
  `init --ci` writes downloads the archive and verifies it against the release's own checksums;
  that step was executed end to end, not merely generated. `v0.1.0` and `v0.1.1` were superseded
  during the rename to the canonical lowercase module path, and `v0.1.0` is retracted in `go.mod`
  because the module proxy caches a path per version immutably.
  not emit a download that 404s (a missing release is not a network flake) and it does not pass.
  Setting that one constant to the first tag, and confirming the published URL resolves, is all that
  remains.

- ~~**TBD-13**~~ — **RESOLVED 2026-08-09: it records, and the earlier blocker was a working
  directory, not a limit of keploy's or ours.** Whether the keploy handoff `capture -engine keploy`
  generates (R-CLI-12) actually records a session end to end. It does. Both generated commands were
  run verbatim, as root, against a real two-service compose stack — a built `api`
  (`container_name: kpdemo-api`, `8080:8080`) whose `/work` makes an outbound HTTP call to an
  `nginx` service `backend`, from which `PlanKeploy` derived SUT and `--container-name` itself.
  `keploy record` (3.6.11) hooked ingress on port 8080 and turned three real curl requests into
  four `kind: Http` testcases under `keploy/test-set-0/tests/` plus a `mocks.yaml` of four mocks —
  two DNS and two HTTP, the latter being the `api`→`backend` dependency call. `keploy test` then
  replayed all four against those mocks: exit 0, 4 passed / 0 failed, and a
  `keploy/reports/test-run-0/test-set-0-report.yaml` with `status: PASSED`. So the testcases and
  the auto-mocks are real recorded traffic, and the `./keploy/` layout `KeployHandoff` describes is
  the layout keploy writes.

  The previous stop — `mounts denied: the path <cwd>/keploy is not shared from the host` — was
  neither keploy's limit nor ours nor even really a restriction: this host runs Docker Desktop,
  whose `FilesharingDirectories` lists `/mnt/ssd`, and bind mounts are refused *only* for a working
  directory outside a shared root (confirmed both ways: the same one-line `docker run -v` mount
  succeeds under `/mnt/ssd` and fails under `/tmp` with that exact message). Run from a shared
  root, keploy's own output mount works. Nothing in the generated command had to change. The one
  genuine environmental requirement that remains is root: unprivileged, record still stops in
  keploy's eBPF setup at `open /proc/sys/kernel/perf_event_paranoid: permission denied`.

  One defect in what we generate was found *by* this run and fixed: `keploy test` waits its own
  fixed `-d/--delay` (default 5s) before the first replayed request, a different flag from
  `--build-delay`, and the handoff's notes named only the latter. At the default, 3 of the 4
  correctly recorded cases failed on connection refused; the same recording passed 4/4 at
  `--delay 25`. The notes now state that wait, and its `--health-url` alternative, so a user does
  not read a replay-timing failure as their application's fault.

  None of this changes what `capture -engine keploy` *does*: `delegate` tier (R-SCOPE-3) still
  means TortureU generates and hands off, and the CLI still states the command as generated rather
  than as run. What is now settled is that the generated command is one that works.

- ~~**TBD-12**~~ — **RESOLVED 2026-08-09: both join a container's network namespace, and the
  translation rule was already in the codebase rather than needing to be invented.** How
  `--db-load` (R-EXE-26) and `--fuzz` (R-EXE-27) reach a SUT or database **R-DC2-3** has put on an
  `internal: true` network. The old answer ran both as host subprocesses against the caller's
  address and failed loudly when it was unreachable — honest, but it meant the two newest
  drive-tier features did not work in the one topology TortureU itself creates.

  The premise that blocked it — "a subprocess dials from the host's own namespace" — was true and
  irrelevant: `K6Runner` is *also* a subprocess, and reaches an internal-only SUT anyway by running
  the tool's own container image with `docker run --network container:<id>`. Both tools have
  official images (`postgres`'s carries `pgbench`; `schemathesis/schemathesis`), so the same route
  was open to both, and the reach paragraphs now in R-EXE-26/R-EXE-27 say which container each
  joins and what happens to the address.

  The "guess" this was deferred over does not exist. For `--fuzz` there is **nothing to translate**:
  it joins the same container k6 joins (`target.service`) and fuzzes `target.base_url` byte for
  byte, so it is pointed at exactly what the load is pointed at, by construction. For `--db-load`
  the only translation is host → `127.0.0.1` with **the port left alone**, which is what
  `inreach.go`'s `containerHopScript` has always done for the orchestrator's own in-stack calls and
  what `load.go`'s package comment already states for k6 — and it is forced, not chosen: an
  internal-only container publishes no port, so the caller's port either *is* the port the server
  listens on or the caller's address was never usable. The container to join is selected by the
  caller's own DSN host, resolved as a compose service the way `fallbackTransport` already resolves
  a URL host — nothing is read out of the compose file, so R-EXE-26's no-guessing refusal is intact.

  Neither runner changes mode silently: each dials the caller's address from the host first and only
  falls back on an actual failure (**R-CLI-6**'s stated rule), so a published-port or
  non-DC-2-isolated stack keeps the host-process path and touches Docker not at all. A DSN host that
  names no running container still fails loudly, exactly as before.

  Verified against a real `internal: true` network, not fakes: a `postgres:16-alpine` and an nginx
  SUT with `docker port` reporting nothing published, where host `pgbench` fails to resolve the DSN
  host and host schemathesis cannot connect — and namespace-joined runs of both reach them,
  sustaining ~1000 tps and reporting the SUT's planted `500` as a finding.

- ~~**TBD-15**~~ — **RESOLVED 2026-08-09: yes, but only when the SUT declares exactly one port —
  and it is the container port, not the published host port.** Detection now records the SUT's own
  listening ports (**R-DET-16**) and `init` emits `base_url` from them under **R-CLI-19**'s
  three-way rule: one port → `http://localhost:<port>`; several → no value and a comment naming
  every candidate; none → no value and a comment saying compose declares none.

  Three decisions worth keeping. First, **which side of `ports:`** — and the obvious answer is the
  wrong one. A `base_url` looks like a host-side address, so the host side (`published`) is what the
  work started from; it is wrong, because nothing dials `base_url` from the host. `run` executes k6
  inside the SUT container's network namespace (`internal/run/load.go`, the **R-DC2-3** fix) and
  `--fuzz` joins the same container, where `localhost:<container port>` is the SUT's own loopback and
  the published port is not bound at all. The other consumers agree: `emit iptables` filters the SUT's
  own `INPUT --dport` with that port and `emit kind` maps it as a `containerPort`. E1's control case
  is the discriminator — it publishes `8081:8080`, the host reading yields `http://localhost:8081`,
  and its committed `torture.yaml` says `8080` with a comment explaining precisely why. Emitting the
  host side would have shipped a `base_url` that is *silently wrong exactly when the mapping is
  asymmetric* — a config that looks detected, runs, and fails every request.

  Second, **several ports are a refusal, not a tie-break.** The measured case is `immich`'s dev
  compose, whose SUT declares `3000` and `24678` (Vite's HMR socket); `docker/awesome-compose`'s
  `react-express-mysql` publishes `80`, `9229` and `9230`, two of them Node's inspector. Any
  "pick the lowest" or "pick the first" rule points load at the wrong process a large fraction of the
  time, and naming all candidates costs the user one uncomment.

  Third, **`expose:` counts.** A port that is exposed but not published is unreachable from the host
  and perfectly reachable from inside the namespace where the load actually runs, so refusing it
  would have withheld a correct answer — `react-express-mongodb`'s backend is the real example. What
  is still refused: a `0` target, a `udp` port, and a port *range*, none of which names one reachable
  TCP port.

  Verified against real repositories rather than fixtures, with the built binary: `flask-redis`
  (one port → `base_url: http://localhost:8000`), `immich`'s `docker-compose.dev.yml` (two → refusal
  naming both), `nginx-golang` (none → refusal, no invented port), and all nine E1 corpus cases,
  each of which now reproduces the `base_url` its committed `torture.yaml` already carried —
  `case8-control` included, at `8080` and not `8081`.

- **TBD-5** — **RESOLVED 2026-08-09: pinned to `grafana/k6:2.1.0`.**

  The blocker was never upstream instability — `grafana/k6-summary` shipped `1.0.0` as the official
  contract. It was a **silent inversion** in the threshold shape, measured against real containers:

  | | k6 0.54.0 | k6 2.1.0 |
  |---|---|---|
  | assertion held | `{"ok": true}` | `false` |
  | assertion broke | `{"ok": false}` | `true` |

  k6 ≤1.x reports *ok*; k6 ≥2.x reports *crossed*. Bumping the pin without touching the parser
  would not have failed loudly — the type assertion drops to `ok=false` and every assertion reads
  as broken. **R-VER-19** now normalises both shapes at the parse boundary and **R-VER-18** refuses
  anything that is neither, so widening did not become "accept whatever turns up".

  With that closed, the bump was made and measured rather than assumed:

  - **E1 is byte-identical** on 2.1.0 — detection 8/8, attribution 5/5 on faulted runs (5/9 over
    every finding), 0 findings on the control. This is expected rather than lucky: **R-VER-10**
    recomputes every verdict from measured values instead of trusting k6's booleans, so the
    threshold shape was never the primary path.
  - **B1 is unaffected by construction** — it drives `fault.Translate` → `fault.Manager` →
    `ToxiproxyApplier` directly and references k6 nowhere, so no published fidelity number depends
    on the pin.
  - Phase markers still arrive on console as `TORTUREU_PHASE_START <phase> <ns>` with
    `source=console` (**R-EXE-8**), and `--summary-export` still emits the flat metric map
    **R-VER-15** reads.

  Two real k6 2.1.0 summaries — one where the threshold held, one where k6 printed "thresholds have
  been crossed" — are committed under `internal/run/testdata/` and asserted against, so the polarity
  claim travels with the test rather than living on the machine that measured it.

  Still not adopted: `--new-machine-readable-summary`. It remains opt-in even in v2, and
  **R-VER-10**'s recomputation makes the richer document unnecessary for the verdict. That is a
  separate decision with no forcing reason behind it.

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

- ~~**TBD-9**~~ — **NARROWED 2026-08-09 to one boundary that is the user's side of the wire.**
  `Finding.Chain` stayed empty because no trace-ingestion pipeline existed, which also capped every
  finding at `correlated` (D-4). The pipeline now exists: **R-VER-13** specifies chain derivation
  from real ingested spans and **R-VER-14** the `caused` upgrade, `internal/trace` queries
  **Jaeger's** query API, and `internal/run` builds the hop list from the parent chain of a real
  span matching the fault's target.

  Three decisions worth keeping. First, **one backend, queried correctly**: Jaeger's
  `/api/traces` returns the whole span tree *with* its `processes` service map in a single
  response, so a chain is derivable from one request — verified against a real
  `jaegertracing/jaeger:2.10.0`. Tempo is identified and refused **by name** (it answers
  `/api/echo` with `echo` and 404s `/api/services` — verified against a real
  `grafana/tempo:2.9.0`), and the OpenTelemetry Collector is refused for a structural reason: it
  is a pipeline with no query API at all, so `otel/opentelemetry-collector` in compose says spans
  are *exported*, never that they are *readable*.

  Second, **every hop value is measured or the chain is empty**. The gate that matters is not
  "traces exist" but "the fault reached the request path": if the target hop's p95 is under twice
  its own fastest-quartile baseline, there is no chain and confidence stays `correlated`. Traces
  present but the dependency never degraded is exactly the case where a plausible-looking invented
  chain would do the most damage.

  Third, the **endpoint** is `TORTUREU_TRACE_URL`, defaulting to `http://localhost:16686` only when
  detection already reported traces (**R-DET-12**) and did not report `lacks:otel` (**R-COV-6**) —
  so a repo with no tracing is never probed, and an explicit URL is the user asserting a backend we
  should try regardless.

  **What remains, precisely**: spans have to exist. TortureU cannot instrument the system under
  test — that is application code the user owns — so on an uninstrumented SUT every gate above
  fails closed and the verdict is exactly what it was before this change. What is *not* left
  unbuilt is any part of reading, matching, or measuring them.

  Also not built, and deliberately: the query window is the **`tortureu` process lifetime**, a
  superset of the run, not the sub-run fault window. `internal/run`'s wall-clock fault start/stop
  lives in its scheduler and is not carried into finding evaluation, so a fault with a `for:`
  shorter than the run is bounded by the *observed* degradation gate above rather than by its own
  clock. Closing that gap means threading the applied-fault timestamps into the verdict, which is
  a change to the run loop, not to ingestion.
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
| **R-EXE-24** (`jitter` is uniform, not Gaussian) | benchmark | Verified by measurement, not assertion: B1 measured a stddev of `26.33ms` for `jitter: 50ms` against the `28.87ms` uniform prediction (`j/√3`), 8.8% off and inside the ±15% tolerance (the row has read 27.95/30.74/26.33ms across runs — sampling spread, not drift). A unit test could only re-assert the constant we chose; the measurement is what establishes Toxiproxy actually behaves this way. Committed under `benchmarks/results/`. |
| **R-DC2-5** (secret-scrub captured traffic on write) | deferred | **TBD-8**: capture does not exist in v0, so there is no write path to scrub. Must ship in the same change as capture, never after — scrubbing retrofitted onto an existing corpus means the unscrubbed cassettes already exist. |

Two requirements — **R-COV-7** and **R-DET-12** — are verified by tests added in the final coverage
pass; if they appear unverified in the report, the citation was lost and that is a defect.

---

## Traceability

`python3 check.py` reports which requirements have tests. It fails on a test citing a requirement
that does not exist here, and on any doc/registry disagreement.
