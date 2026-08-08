# TortureU — Tool Landscape R&D

**Goal:** one CLI, one config, one verdict. Devs point it at their codebase; it detects the
stack and drives every layer of backend torture — load, faults, replay, correctness, security —
without them wiring five tools together. Usable directly by humans and by coding agents
(Claude Code / Codex / Cursor) through a single MCP surface.

**Status:** built and measured. 19 domains · 154 tools · 2 constraints · 9 decisions.
This file is the **survey and rationale** — the *why*. `SPEC.md` is normative and `BENCHMARKS.md`
carries the measured results; where any of them disagree with this file, they win.
**Date:** 2026-08-08

---

## Legend

**Tier** — how TortureU relates to the tool. Machine-readable form lives in `registry.yaml`.

| Tier | Meaning | Count |
|---|---|---|
| **drive** | We execute it, on our clock, folded into one verdict. Needs co-execution. | 30 |
| **delegate** | We generate its config/command and hand off. Real output, separate timing. | 34 |
| **know** | We name it, say when it applies, point at it. Zero integration cost. | 90 |

The P0/P1/P2/SKIP columns below are **build order within a tier**, not coverage. Coverage is
`drive`+`delegate`+`know` = all 154 tools, which is what makes "all in one place" true (see
[Compliance](#compliance--all-in-one-place)).

**Effort** — adapter cost: **S** = config/exec wrapper (<1d) · **M** = needs state/lifecycle mgmt (2–5d) · **L** = real subsystem (1w+)

**Own?** — `drive` = we execute it · `emit` = we generate its config/script and hand off · `read` = we consume its output only

---

## 1. Load generation (synthetic)

| Tool | Lang | License | Tier | Effort | Own? | Notes |
|---|---|---|---|---|---|---|
| **k6** | JS (Go core) | AGPL-3 | **P0** | S | drive | Default engine. Open-model `ramping-arrival-rate` = true spike sim. Native Prometheus RW output. Already ships `k6 x agent` / `k6 x mcp` — **do not rebuild that, compose with it.** |
| **Vegeta** | Go | MIT | **P0** | S | know | Constant-rate smoke check. `tortureu smoke` implements its *model* in-process rather than driving the binary: reaching a DC-2-isolated SUT needs a custom dialer a subprocess cannot use (R-CLI-6). |
| **Gatling** | Scala/Java/JS | Apache-2 | P1 | M | emit | Highest throughput/generator. JVM lifecycle + report parsing is the cost. |
| **Locust** | Python | MIT | P1 | M | drive | Python-native teams; distributed master/worker adds lifecycle work. |
| **JMeter** | XML/Groovy | Apache-2 | P1 | M | emit | Only reason: protocol breadth (JDBC/JMS/LDAP/SOAP/FTP). Thread-per-VU, heavy. |
| **Artillery** | YAML/JS | MPL-2 | P2 | S | emit | Lambda/Fargate fan-out is the one thing k6 OSS lacks. |
| **wrk2** | C + Lua | Apache-2 | P2 | S | drive | Correct coordinated-omission handling; niche now that k6 has open model. |
| **oha / bombardier / hey** | Go/Rust | MIT | P2 | S | drive | Redundant with Vegeta. Pick one. |
| **NBomber** | C#/F# | Apache-2 | P2 | M | emit | .NET shops only. |
| **Tsung** | Erlang | GPL-2 | SKIP | — | — | Erlang toolchain cost >> value. |
| **BlazeMeter / LoadRunner / NeoLoad / k6 Cloud** | — | commercial | SKIP | — | — | Managed generators. Out of scope for a local-first CLI. |

> **Decided: k6 + Vegeta only at `drive`.** Every extra engine multiplies the verdict-normalization
> surface (§12) without adding a scenario we can't express. Others are `delegate` (we emit their
> config) or `know` — see `registry.yaml`.

---

## 2. Protocol-specific load

| Target | Tool | Tier | Effort | Notes |
|---|---|---|---|---|
| gRPC | k6 gRPC module · **ghz** | **P0** | S | k6 covers it; ghz for standalone |
| WebSocket / SSE | k6 `ws` · Artillery | **P0** | S | Long-lived conns are a distinct failure class (fd exhaustion) |
| GraphQL | k6 + custom · easygraphql-load-tester | P1 | S | Mostly HTTP POST; query-depth abuse is the real test |
| PostgreSQL | **pgbench** · HammerDB | **P0** | S | Direct DB saturation, independent of app |
| MySQL | **sysbench** · HammerDB | P1 | S | |
| Redis | **redis-benchmark** · memtier_benchmark | P1 | S | |
| Kafka | **xk6-kafka** · kafka-producer-perf-test | P1 | M | Consumer-lag scenarios need their own verdict shape |
| MongoDB | mongo-perf · YCSB | P2 | M | |
| Generic KV/NoSQL | **YCSB** | P2 | M | Standard workloads A–F, good baseline vocabulary |
| Elasticsearch | **Rally** | P2 | M | |
| MQTT | emqtt-bench · mqtt-stresser | P2 | S | IoT backends |
| S3 / object | **warp** (MinIO) · s3-benchmark | P2 | S | |
| DNS | dnsperf · flamethrower | SKIP | — | Infra concern, not app backend |
| TCP/UDP raw | iperf3 · tcpkali | P2 | S | Bandwidth ceiling calibration for §11 |

---

## 3. Network fault injection

| Tool | Scope | Tier | Effort | Notes |
|---|---|---|---|---|
| **Toxiproxy** | TCP proxy | **P0** | M | **The core of v0.** latency, jitter, bandwidth, slow_close, timeout, slicer, limit_data, reset_peer. Local, no K8s, HTTP API = scriptable on a timeline. |
| **tc / netem** | kernel | P1 | M | delay/loss/dup/reorder/corrupt/rate. Needs NET_ADMIN; container-scoped via Pumba. |
| **Pumba** | Docker | P1 | S | netem + container kill/pause/stop. Natural fit for compose stacks. |
| **iptables / nftables** | kernel | P1 | S | Blackhole vs RST — very different client behaviour, both worth exposing. |
| **Envoy / Istio fault filter** | mesh | P2 | M | HTTP-level abort/delay by %. Only if user already runs a mesh. |
| **Linkerd fault injection** | mesh | P2 | M | Same. |
| **Blockade** | Docker | P2 | S | Partitions; overlaps Pumba. |
| **Comcast** | netem wrapper | SKIP | — | Thin wrapper over tc, unmaintained-ish. |
| **Muxy** | proxy | SKIP | — | Superseded by Toxiproxy. |

> **This is the gap.** Toxiproxy is local but has **no scheduler** — you drive it by hand.
> Chaos Mesh / Litmus have schedulers but need Kubernetes. Nothing lets a dev on a laptop say
> *"5k rps ramp while Postgres gains 300ms at t=60s and Redis dies at t=90s."*
> Time-synchronized load × fault, off-K8s, is TortureU's reason to exist.

---

## 4. Host / resource fault injection

| Tool | Scope | Tier | Effort | Notes |
|---|---|---|---|---|
| **stress-ng** | CPU/mem/IO/fd/futex/sched | **P0** | S | 300+ stressors. Noisy-neighbour + OOM proximity in one binary. |
| **cgroups v2** | hard limits | **P0** | M | Cap CPU/mem/IO on the SUT to force real contention. Compose `deploy.resources` covers most of it. |
| `kill -STOP` / `-9` | process | **P0** | S | Most underrated fault. Pause ≠ crash ≠ graceful shutdown — three distinct client behaviours. |
| **fio / dd** | disk | P1 | S | IOPS ceiling, fsync latency — where DBs actually die. |
| **libfaketime** | clock | P1 | M | Clock skew breaks JWT exp, cache TTL, leader leases. High bug yield. |
| **chaosblade** | host+container+K8s+JVM | P1 | M | Alibaba, CNCF. JVM-level injection is genuinely unique. |
| **charybdefs / dm-flakey** | FS/block | P2 | L | Injects IO errors + slowness. Requires FUSE/devmapper privileges. |
| **libfiu** | syscall | P2 | L | Syscall-level failure injection. Powerful, invasive. |
| **stress** (legacy) | — | SKIP | — | stress-ng supersedes. |

---

## 5. Platform chaos (K8s / cloud)

| Tool | Tier | Effort | Notes |
|---|---|---|---|
| **Chaos Mesh** | P1 | M | CNCF. 17 attacks incl. kernel panic, clock skew, DNS chaos. CRD-driven = we emit YAML. Has its own MCP already. |
| **LitmusChaos** | P2 | M | CNCF. Experiment hub, GameDay workflows, existing k6 load-chaos integration worth studying. |
| **Chaos Toolkit** | P2 | M | Vendor-neutral JSON experiments + driver model. Possible *escape hatch* backend rather than a peer. |
| **AWS FIS** | P2 | M | Spot interruption / AZ failure are un-simulatable locally. Real value, narrow audience. |
| **kube-monkey / Chaos Monkey** | SKIP | — | Random pod kill = one line of Chaos Mesh. |
| **PowerfulSeal** | SKIP | — | Overlaps Chaos Mesh, less active. |
| **Azure Chaos Studio / GCP** | SKIP | — | Add on request. |
| **Gremlin / Steadybit / Harness CE** | SKIP | — | Commercial. Steadybit's MCP is prior art worth reading, not wrapping. |
| **ChaosEater** (NTT) | SKIP | — | **Prior art, study closely** — LLM runs hypothesis→experiment→analyze→improve over K8s. Closest thing to our §17 loop. |

> K8s chaos is a *deployment target*, not v0. Compose-first, cluster-second. Emitting Chaos Mesh
> CRDs from the same `torture.yaml` is the clean upgrade path — same scenario, different executor.

---

## 6. Traffic capture & replay

| Tool | Tier | Effort | Notes |
|---|---|---|---|
| **GoReplay** | P1 | M | Capture prod HTTP, replay 1x–100x. Simple, free, replay-only (no mocks/transforms). |
| **Keploy** | P1 | M | eBPF capture → test cases **+ auto-mocks for every downstream dep**, zero code change. HTTP/gRPC/WS/GraphQL. Apache-2. **Integrate, never reimplement — eBPF capture is a product on its own.** |
| **mitmproxy** | P2 | S | Interactive capture + scriptable rewrite. Good dev-loop tool. |
| **VCR / go-vcr / Polly.js / Betamax** | P2 | S | Per-language cassettes; belongs to the app's test suite, not our CLI. |
| **Envoy request mirroring** | P2 | S | Mesh-level shadow. Config emit only. |
| **Diffy** (Twitter) | P2 | M | Old-vs-new response diffing. Regression, not load. |
| **tcpreplay** | SKIP | — | Raw pcap wire replay; too low-level for app backends. |
| **Speedscale** | SKIP | — | Commercial. Direct competitor to the whole category. |

---

## 7. Correctness under chaos

| Tool | Tier | Notes |
|---|---|---|
| **Jepsen + Elle** | SKIP (document) | Gold standard for linearizability/serializability under partition. **Separate discipline** — Clojure, bespoke per-system harness, days per run. Wrapping buys nothing. Point users at it. |
| **Antithesis** | SKIP (document) | Deterministic hypervisor over a whole Compose stack, autonomous bug search, byte-exact repro. Commercial. **Study the UX — it's the aspirational ceiling.** |
| **madsim** | SKIP | Rust-only deterministic async runtime. In-process, not a CLI concern. |
| **Porcupine** | P2 | Go linearizability checker — *could* consume our request/response history to assert consistency. The one wrappable piece of this domain. |
| **Maelstrom** | SKIP | Teaching/dev workbench for your own distributed toy. |
| **TLA+ / Alloy / P** | SKIP | Design-time formal methods. Different phase entirely. |
| **In-house DST** | SKIP (v0) | Seeded clock/net/disk sim (FoundationDB, TigerBeetle, WarpStream, S2 pattern). Requires owning the runtime — impossible for arbitrary user code. |

> Whole domain is **document-and-defer**. These tools require deep per-system integration; a generic
> CLI can't deliver them. Exception: **Porcupine**, if we already hold the full request history.

---

## 8. API fuzzing / property testing

| Tool | Tier | Effort | Notes |
|---|---|---|---|
| **Schemathesis** | **P0** | S | Property-based fuzz straight from OpenAPI/GraphQL. Cheapest 500-finder in existence. If a spec exists, run it — near-zero config. |
| **RESTler** (Microsoft) | P1 | M | Stateful REST fuzzing, infers request *sequences*. Finds what stateless fuzzers can't. |
| **Dredd** | P2 | S | Spec conformance. Overlaps Schemathesis. |
| **AFL++ / libFuzzer / cargo-fuzz / go-fuzz** | SKIP | — | Binary/lib coverage-guided fuzzing. Belongs in the app's own CI. |
| **Hypothesis / fast-check / proptest** | SKIP | — | In-process property testing. Not a CLI concern. |
| **boofuzz** | SKIP | — | Protocol fuzzing, security-team tool. |

---

## 9. Dependency virtualization / mocking

| Tool | Tier | Effort | Notes |
|---|---|---|---|
| **WireMock** | **P0** | M | HTTP stub + **fault + delay + proxy-record**. The fault modes matter: we need a *degradable* fake for third-party APIs we must not hammer. |
| **Keploy mocks** | P1 | M | Auto-derived from real traffic — strictly better than hand-written when capture is available. |
| **Hoverfly** | P1 | S | capture/simulate/synthesize modes + delay injection. Lighter than WireMock (Go, single binary). |
| **LocalStack** | P1 | M | AWS emulation with failure injection. Essential if the SUT talks S3/SQS/DynamoDB. |
| **Prism** (Stoplight) | P2 | S | Mock server from OpenAPI. Trivially replaced by WireMock+spec. |
| **MockServer** | P2 | S | Overlaps WireMock. |
| **Mountebank** | P2 | M | Multi-protocol imposters (HTTP/TCP/SMTP). Only if non-HTTP deps matter. |

> Torture without dependency virtualization is unethical at best — a 100x replay against a real
> partner API is an outage you cause someone else. **Mocking is a v0 safety requirement, not a nicety.**

---

## 10. Environment provisioning

| Tool | Tier | Effort | Notes |
|---|---|---|---|
| **Docker Compose** | **P0** | M | The lowest common denominator. `tortureu init` parses it to build the service graph + dep endpoints. **This is the detection substrate.** |
| **Testcontainers** | P1 | M | Real deps in-test, all major languages, pause/kill mid-transaction. Library not CLI — we'd *emit* helpers. |
| **kind / k3d / minikube** | P2 | M | Local K8s for the §5 path. |
| **Nix / devenv** | SKIP | — | Toolchain reproducibility, orthogonal. |
| **Dev Containers** | SKIP | — | Editor concern. |
| **Signadot / vCluster** | SKIP | — | Ephemeral preview envs, commercial/K8s-only. |

---

## 11. Observability & measurement

| Layer | Tools | Tier | Notes |
|---|---|---|---|
| Metrics | **Prometheus** · VictoriaMetrics | **P0** | k6 writes here natively. Our verdict engine reads here. |
| Traces | **OpenTelemetry** · Jaeger · Tempo | **P0** | **The correlation key.** A trace spanning the fault window is how we prove *which* dep caused the p99. Without traces the verdict is a guess. |
| Logs | Loki · Vector | P1 | Error-rate + retry-storm detection. |
| Continuous profiling | **Pyroscope / Parca** | P1 | CPU/alloc flamegraph diffed across the fault window = root cause for free. |
| On-demand profiling | pprof · async-profiler · py-spy · perf | P1 | Language-specific; trigger on threshold breach. |
| Kernel | bpftrace · bcc | P2 | fd/conn/syscall visibility when the app-level story doesn't add up. |
| Auto-instrument | **Pixie** | P2 | eBPF, zero-code telemetry. Big win when the SUT has no OTel. |
| Dashboards | Grafana | P1 | Ship a canned dashboard; also has an MCP for §17. |

> Verdict quality is **capped by observability**. If the SUT emits nothing, TortureU can only say
> "p99 got worse." The `init` step must detect OTel presence and say so loudly.

---

## 12. Assertions / SLO gating

| Tool | Tier | Effort | Notes |
|---|---|---|---|
| **k6 thresholds** | **P0** | S | Native pass/fail on p95/error-rate. Our primary gate in v0. |
| **OpenSLO** | P1 | S | Vendor-neutral SLO spec — **strong candidate for `torture.yaml`'s assertion block** rather than inventing syntax. |
| **Prometheus rules** | P1 | M | Arbitrary PromQL assertions over the run window. |
| **Sloth / Pyrra** | P2 | S | SLO-as-code → Prom rules. Generator, we can emit. |
| **Bencher / Conbench** | P1 | M | Perf regression tracking across commits — turns one-shot runs into a trend. |
| **hyperfine** | P2 | S | Statistically sound CLI benchmarking. |
| **Keptn** | SKIP | — | Heavy platform for what a PromQL query does. |

> **The normalized verdict schema lives here and is the actual product.** k6 reports p99, Toxiproxy
> caused the fault, logs hold the retries — nothing joins them. One schema
> (`PASS/FAIL + broken SLO + causing fault + failure chain`) is what makes this agent-usable.

---

## 13. Data & fixtures

| Tool | Tier | Effort | Notes |
|---|---|---|---|
| **Faker / gofakeit / Bogus** | P1 | S | Synthetic payloads for generated scenarios. |
| **Great Expectations / Soda** | P1 | M | **Post-torture data integrity assertions.** "Did chaos corrupt anything?" — the highest-value question nobody asks. |
| **Snaplet / Neosync / Tonic** | P2 | M | Prod subsetting + anonymization. Compliance-gated. |
| DB seeds (Flyway/Liquibase/pgbench scripts) | P1 | S | Deterministic starting state = reproducible runs. |

---

## 14. Contract & compatibility

| Tool | Tier | Effort | Notes |
|---|---|---|---|
| **oasdiff** | P1 | S | OpenAPI breaking-change detection. Cheap, high signal. |
| **Buf breaking** | P1 | S | protobuf/gRPC compat. Cheap, high signal. |
| **Pact** | P2 | L | Consumer-driven contracts + broker + can-i-deploy. Whole workflow of its own. |
| **Spring Cloud Contract** | SKIP | — | JVM-specific Pact variant. |

---

## 15. Security under load

| Tool | Tier | Notes |
|---|---|---|
| **slowhttptest** | P1 | Slowloris / resource exhaustion — genuinely a *load* concern, belongs with us. |
| **OWASP ZAP** | SKIP (document) | Full DAST workflow, its own discipline and CI job. |
| **Nuclei** | SKIP (document) | Template vuln scanning. Different phase. |
| **Semgrep / CodeQL** | SKIP (document) | Static. But: could seed our fault matrix by finding missing timeouts/retries. Interesting later. |
| **sqlmap** | SKIP | Pentest tool, not a dev toolkit component. |

> Security is a **separate product**. Everything except slowhttptest is document-and-point-elsewhere.
> Wrapping ZAP makes us a worse ZAP.

---

## 16. Orchestration & CI

| Tool | Tier | Notes |
|---|---|---|
| **GitHub Actions / GitLab CI** | **P0** | Where runs actually happen. Ship an action + a machine-readable exit contract. |
| **Testkube** | SKIP (competitor) | K8s-native orchestration of k6/JMeter/Playwright/Postman. **Closest structural competitor — but cluster-only.** Our wedge is local-first. |
| **Argo Workflows / Tekton** | P2 | Generic DAG. Only if users ask. |
| **Bencher** | P1 | See §12. |

---

## 17. Agent-facing surfaces (prior art — do not rebuild)

| Surface | Provider | Implication |
|---|---|---|
| `k6 x agent` / `k6 x mcp` / `k6 x docs` / `k6 x explore` | Grafana (k6 2.0) | **Registers skills + MCP into Claude Code, Codex, Cursor, Copilot, Cline, OpenCode.** The load-testing agent layer is done and free. Compose with it; do not compete. |
| Steadybit MCP | Steadybit | First chaos MCP. Read the tool schema for vocabulary. |
| Chaos Mesh MCP | community | K8s chaos via natural language. |
| Grafana MCP | Grafana | Query metrics/logs/traces — a verdict engine could lean on this instead of building readers. |
| Playwright MCP | Microsoft | Frontend; out of scope. |
| ChaosEater | NTT | LLM-driven hypothesis→experiment→analyze→improve. **Closest prior art to our closed loop, but K8s-manifest-only — not application code.** |

**Consequence for positioning:** every tool vendor is building their own agent layer.
A wrapper whose value is "we call k6 for you" gets obsoleted one release at a time — it already
happened once. Durable value must live in **composition, correlation, and verdict**, which no
single vendor can own because it spans their competitors.

---

## Compliance — "all in one place"

**Claim:** a dev points one CLI at their codebase and gets every backend-torture capability that
exists, at the right depth for each.

**Audit against the claim.** `drive` = we co-execute it. `del` = we generate its config and hand
off. `know` = we name it, say when it applies, point at it. **Every domain has non-zero coverage.**

| # | Domain | Tools | drive | del | know | Front door |
|---|---|---|---|---|---|---|
| 1 | Load generation | 15 | 2 | 3 | 10 | `run` `smoke` `emit` |
| 2 | Protocol load | 15 | 3 | 4 | 8 | `run` `emit` |
| 3 | Network faults | 7 | 1 | 3 | 3 | `run` `emit` |
| 4 | Host/resource faults | 9 | 3 | 2 | 4 | `run` `emit` |
| 5 | Platform chaos | 10 | 0 | 1 | 9 | `emit chaosmesh` `suggest` |
| 6 | Capture & replay | 8 | 0 | 2 | 6 | `capture` `replay` |
| 7 | Correctness | 7 | 0 | 1 | 6 | `emit porcupine` `suggest` |
| 8 | API fuzzing | 7 | 1 | 1 | 5 | `run --fuzz` |
| 9 | Dep virtualization | 7 | 1 | 3 | 3 | `egress:` (DC-2) |
| 10 | Environments | 6 | 1 | 2 | 3 | `init` |
| 11 | Observability | 11 | 2 | 3 | 6 | auto-detect · `emit dashboard` |
| 12 | SLO gating | 6 | 2 | 1 | 3 | `assert:` |
| 13 | Data & integrity | 9 | 2 | 3 | 4 | `assert: sql` `emit soda` |
| 14 | Contracts | 4 | 0 | 2 | 2 | `check contracts` |
| 15 | Security | 5 | 0 | 1 | 4 | `emit slowloris` `suggest` |
| 16 | Orchestration | 4 | 1 | 1 | 2 | `init --ci` |
| 17 | Agent surfaces | 5 | 1 | 0 | 4 | `mcp` |
| 18 | **Event-driven / async** | 8 | 4 | 2 | 2 | `run` `assert:` |
| 19 | **Resilience config audit** | 8 | 4 | 0 | 4 | `doctor` |
| | **Total** | **154** | **30** | **34** | **90** | |

> Counts are generated from `registry.yaml`, not hand-maintained — an earlier hand-count in this
> table was wrong by 14 tools. If they drift again, the registry is the truth.

**Verdict: compliant.** 19/19 domains reachable from the CLI. The three tiers are honest about
*depth* rather than pretending uniform integration:

- **26 `drive`** — everything that must run **on one clock**. Co-execution is the whole product;
  these are the only tools where wrapping adds something no vendor can.
- **33 `delegate`** — real tools, real output, our config. No timing integration because they don't
  need it (a contract check is not a load phase).
- **78 `know`** — `tortureu doctor` / `suggest` name them with a trigger condition. Zero
  integration cost, and this is where the earlier design was genuinely non-compliant: the knowledge
  sat in this document, which a dev in a terminal never reads. `registry.yaml` moves it into the CLI.

**What changed to reach compliance**

1. **Three tiers instead of two.** SKIP said "we decided not to." `know` says "here's what to use
   instead, and when" — strictly more useful, and it costs one registry file.
2. **`registry.yaml` is the artifact.** 137 entries, each with a `when:` predicate evaluated against
   `describe_system()`. `tortureu suggest` is then a filter, not a feature.
3. **Two domains were missing entirely** (§18 async, §19 resilience audit). Round-2 research found
   both. §18 is genuinely new capability; §19 is a gap in the *market*, not just our design — no
   resilience linter exists, and D-3 + D-9 already give us most of one.

**Honest limits, stated rather than papered over:**

- `know`-tier tools are pointed at, not run. That is the correct depth — wrapping ZAP makes us a
  worse ZAP — but it is not "TortureU runs everything," and the docs should never imply it.
- §7 correctness stays `know` apart from Porcupine. Jepsen and Antithesis need per-system harnesses
  a generic CLI cannot supply. Claiming otherwise would be the dishonest kind of all-in-one.
- Coverage is a function of `registry.yaml` staying current. It is data, not code, precisely so it
  can be updated without a release.

**Build order** (unchanged by any of the above — `drive` tier, in dependency order):

```
1  init      compose + lockfile -> torture.yaml + egress manifest   (D-3, DC-2)
2  run       k6 + Toxiproxy on one clock -> verdict                 (§1,3,11,12)
3  smoke     vegeta constant-rate sanity check                      (§1)
4  doctor    resilience audit + registry coverage report            (§19, know tier)
5  mcp       five tools, disjoint nouns                             (DC-1)
6  check     contracts: oasdiff + buf breaking                      (§14)
7  emit      delegate tier, one adapter at a time                   (35 tools)
8  capture   keploy/goreplay ingest                                 (§6)
9  replay    capture -> load, DC-2 rate ceiling enforced            (§6)
```

All nine verbs appear as `how:` values in `registry.yaml`; that file and this list are checked
against each other.

---

## 18. Event-driven / async torture

*Added in R&D round 2. Was wrongly buried inside §2 "protocol load" — it is its own domain.*

Async backends fail in ways synchronous load tests **cannot express**. A load generator measures
request→response; a queue consumer has neither. The failure modes are entirely different:

| Failure mode | Why load testing misses it | Tier |
|---|---|---|
| **Poison pill** | One malformed message blocks an entire partition indefinitely. No rps figure shows this. | drive |
| **Duplicate delivery** | Tests idempotency *for real* rather than by code inspection. Duplicate charges live here. | drive |
| **Rebalance storm** | Consumers killed mid-batch, repeatedly — reprocessing + ordering violations. | delegate |
| **Unbounded consumer lag** | The async equivalent of p99, and the actual SLO for a queue-backed system. | drive |
| **DLQ overflow** | What happens when the failure-handling path itself fails. | delegate |
| **Broker down / partition / leader election** | Toxiproxy in front of the broker covers all three. | drive |

**Cost is low:** Toxiproxy already sits in front of dependencies for §3, and a broker is just
another dependency. Most of this is *scenario vocabulary*, not new machinery — new `inject:` verbs
(`poison_pill`, `duplicate`) plus a PromQL assertion for lag.

**Consequence:** `assert:` must accept queue-shaped SLOs (`consumer_lag`, `dlq_depth`), not only
HTTP ones. Already satisfied by D-2's `promql:` escape — no schema change needed.

---

## 19. Resilience configuration audit

*Added in R&D round 2.*

**Finding: no linter exists for resilience antipatterns.** The four patterns that prevent cascade
failure (timeout, retry+backoff, circuit breaker, bulkhead) are universally recommended and nothing
checks whether you actually configured them.

We are unusually well positioned for this, and it costs almost nothing:

- **D-3** already detects client libraries from the lockfile.
- **D-9** already enumerates each library's resilience knobs as verdict `candidates`.
- Checking *"is a timeout actually set on this client"* is then a small static check over a known
  library's construction site — not general static analysis.

| Check | Why it matters |
|---|---|
| **Missing timeout** | Retries and circuit breakers are useless behind an infinite timeout. The single highest-yield check. |
| **Uncapped retry** | Retries with no limit/backoff/jitter *are* an overload source, not a mitigation. |
| **Retried non-idempotent op** | Duplicate charges, duplicate orders. Highest-severity finding available. |
| **No breaker on cascade path** | One slow dep takes the whole service down. |

Surfaces as `tortureu doctor`, and each finding **seeds a matching experiment**: a missing timeout
on the Postgres client produces the exact `pg_slow` fault that proves it. Static hint, dynamic
proof — which is stronger than either alone and closes the loop from *audit* to *experiment* to
*verdict*.

This does **not** violate D-3's no-source-analysis cap: we look only at known libraries' known
construction sites, not at arbitrary control flow.

---

## Design constraints (locked)

Two failure modes fall out of the survey. Both are fixed by **narrowing the surface so the
conflict can't occur**, not by adding coordination logic. Neither adds a component.

### DC-1 — One vocabulary per question (resolves §17)

**Problem:** Grafana's `k6 x agent` already registers k6 skills + MCP into Claude Code, Codex,
Cursor, Copilot, Cline, OpenCode. If TortureU also exposes a load-testing tool, an agent has two
ways to answer one question and picks wrong ~half the time.

**Rule:** TortureU never exposes a tool whose noun is a k6 concept.

| Our nouns | k6's nouns |
|---|---|
| `experiment`, `fault`, `slo`, `verdict` | `script`, `test`, `threshold` |

Zero overlap → the tool sets are complementary and neither needs to know the other exists.

```
tortureu mcp exposes exactly:
  describe_system()      -> services, deps, external egress, observability coverage
  propose_experiments()  -> ranked scenarios given the detected topology
  run_experiment(name)   -> normalized verdict
  explain_failure(id)    -> failure chain: fault -> symptom -> code location
  emit_k6_script(name)   -> ESCAPE HATCH: raw script when torture.yaml can't express it
```

Note what is absent: nothing that generates, validates, or runs a k6 script. **The agent writes
`torture.yaml`; we compile to k6.** When raw k6 is genuinely needed, `emit_k6_script` hands off and
k6's own skills become exactly the right tool — escape hatch, not default path.

**Second half:** `tortureu init` detects a registered k6 MCP and appends a division-of-labor line
to the project's `CLAUDE.md` / `AGENTS.md`:

> Load+fault scenarios: use `tortureu`. Raw k6 scripting: use k6 tools, after `emit_k6_script`.

Three lines of generated text. Deliberately **not** detecting-and-unregistering someone else's MCP —
that's hostile (users installed it on purpose) and brittle.

**Generalizes:** the same noun rule applies to every vendor MCP we meet (Steadybit, Chaos Mesh,
Grafana). We own `experiment`/`verdict`; they own their own primitives.

### DC-2 — Default-deny egress (resolves §9)

**Problem:** dependency mocking framed as a feature is opt-in, and opt-in safety fails. A 100x
replay against a real partner API is an outage *we* cause *someone else*.

**Rule:** unclassified external egress is a hard error. The run refuses to start.

`tortureu init` walks the compose graph and emits an egress manifest — every external host the SUT
touches, each requiring explicit classification:

```yaml
# torture.yaml
egress:
  default: deny          # unclassified host -> connection refused, run aborts
  hosts:
    postgres:5432:  { class: internal }              # in compose graph
    redis:6379:     { class: internal }
    api.stripe.com: { class: mock, from: capture }   # WireMock, replayed
    api.twilio.com: { class: mock, from: spec }
    telemetry.vendor.io: { class: block }            # silently dropped
    # api.partner.com: UNCLASSIFIED -> run refuses to start
```

Three enforcement points:

1. **Network-level (the actual guarantee).** SUT runs in a docker network with no default route;
   the only path out is our proxy. Unclassified host → refuse connection, abort, name the host in
   the error. This is topology, not a policy check that can be forgotten.
2. **Multiplier guard.** Hosts classed `real` get a hard rate ceiling; replay above 1x against one
   requires an explicit flag. Blast radius scales with the multiplier, and the multiplier is exactly
   what makes replay attractive — so guard the dangerous knob, not the safe one.
3. **Capture hygiene.** Recorded traffic (§6) is secret-scrubbed **on write, not on replay.** Auth
   headers and tokens never reach a cassette that might get committed.

The `deny` default is the whole thing; the rest is ergonomics around it.

**Cost:** ~zero. The proxy already exists for §3 fault injection — same binary, two jobs.

---

## Decisions

### D-1 — Config surface: one flat `torture.yaml`, no includes

Single file: `egress` + `load` + `faults` + `assert`. No includes, no per-domain files, no
inheritance until a real repo outgrows it. A scenario that doesn't fit on one screen is a scenario
nobody can reason about at 3am — and includes are the cheapest thing to add later if we're wrong.

### D-2 — Assertion syntax: k6 threshold expressions + a PromQL escape hatch

Rejected OpenSLO for v0. It's a full SLO-management spec (Service/SLO/SLI objects, K8s-CRD shaped,
built for long-window burn-rate alerting) — we need *did this 3-minute run pass*, which is a
different question. Adopting it means a translation layer on both ends and a vocabulary users don't
already know.

We drive k6 anyway, so **k6 threshold syntax is our assertion language, verbatim** — zero
translation, and it's the syntax our users already have in their heads:

```yaml
assert:
  - http_req_duration: ["p(95)<500", "p(99)<1500"]
  - http_req_failed:   ["rate<0.01"]
  - promql: 'sum(rate(app_retries_total[30s])) < 100'   # escape hatch: non-k6 signals
  - promql: 'pg_stat_activity_count / pg_settings_max_connections < 0.9'
```

The `promql:` form covers everything k6 can't see — retry storms, pool saturation, queue depth,
post-run data integrity. Two forms, both already-known syntax, no invented DSL.

Revisit OpenSLO only if users ask for SLOs shared across tools outside TortureU.

### D-3 — Detection depth: compose + lockfile only

`tortureu init` parses `docker-compose.yml` (service graph, ports, images → dep types) and the
lockfile/manifest (`package.json`, `go.mod`, `pyproject.toml`, `Gemfile`, `pom.xml`) for
client-library detection. **No source analysis.** Full dep-endpoint extraction from arbitrary source
is where this quietly becomes a 6-month static-analysis project with per-language rot.

Gaps get filled by the agent, which is *better* at reading unfamiliar source than any heuristic we'd
ship — that's the whole point of `describe_system()` returning known-unknowns rather than guesses.
Anything `init` couldn't classify lands in the egress manifest as `UNCLASSIFIED`, which per **DC-2**
blocks the run until a human or agent resolves it. Detection gaps fail loud, never silent.

### D-4 — Verdict without OTel: `caused` vs `correlated` confidence

Honest degraded mode, and better than expected — **we schedule the faults, so we own the independent
variable.** We know precisely that `pg_latency_300ms` was active t=60–90s. That makes time-window
attribution a real experiment, not a guess.

Every verdict carries a confidence level:

| Level | Requires | Meaning |
|---|---|---|
| `caused` | traces spanning the fault window | Request path proven through the degraded dep |
| `correlated` | one fault active at a time | SLO broke inside the fault window, single candidate cause |
| `ambiguous` | overlapping faults, no traces | ≥2 candidate causes, both reported, neither claimed |

Consequence for scenario design: **without OTel, prefer sequential single-fault schedules** —
`correlated` at least stays honest. Overlapping faults without traces produce `ambiguous`, which is
nearly useless. `tortureu init` reports observability coverage up front and says which confidence
level this repo can currently reach.

### D-5 — k6 MCP relationship: **resolved by DC-1**

Both MCPs coexist. Disjoint nouns, no competition. See DC-1.

### D-6 — Language: Go

Single static binary (the whole pitch is "one CLI, no runtime to install"). Native Docker/Compose
SDK, which is our detection substrate and our network-isolation enforcement point (**DC-2**).
Matches the ecosystem we're wrapping — Toxiproxy, k6, Pumba, GoReplay, Testkube are all Go, so
adapters can link libraries instead of shelling out where it matters. Rust buys nothing here (we're
IO-bound orchestration, not compute); TS costs us the static binary.

---

### D-7 — Fault scheduling: phase-relative, with absolute escape

Faults anchor to **load phases**, not wall-clock:

```yaml
faults:
  - at: ramp_complete        # phase anchor (preferred)
  - at: ramp_complete+30s    # anchor + offset
  - at: t=90s                # absolute escape hatch
```

Absolute timelines rot the moment anyone edits the load profile — every fault silently lands in the
wrong phase and the run still "passes." Phase anchors survive profile changes, which is the common
edit. Anchors come free: k6 stages already define `ramp_start`, `ramp_complete`, `peak`,
`ramp_down`, `end`.

### D-8 — State between runs: reset by default, user supplies the command

Verdicts must be comparable across commits or §12 trend tracking is meaningless, so **reset is the
default** and `--no-reset` is the opt-out for fast iteration.

We do not build DB snapshotting. `torture.yaml` takes a reset command; default is the obvious one:

```yaml
reset:
  command: docker compose down -v && docker compose up -d --wait
  # override with a snapshot restore, seed script, whatever is faster for your stack
```

Anyone whose reset is too slow already knows the fast path for their own database better than we do.
Shipping a snapshot subsystem to save them a config line is exactly the trap.

### D-9 — `explain_failure` returns a failure chain, not a `file:line`

The last mile — trace → *the timeout constant that needs changing* — has no existing tool because
it's per-language, per-library, per-codebase. Building a static analyzer for it re-opens everything
**D-3** deliberately closed.

**We don't map it. We hand the agent a well-scoped search.** `explain_failure` returns the fault,
the observed symptom, the affected span, and the *candidate config surface* — the client library
already identified from the lockfile (D-3) and its known knobs:

```json
{
  "fault": "pg_latency_300ms @ ramp_complete",
  "symptom": "http_req_duration p99 4.2s (SLO 1.5s)",
  "chain": ["checkout-api", "pgx pool acquire", "queue depth 47/20"],
  "confidence": "caused",
  "candidates": [
    {"library": "jackc/pgx", "source": "go.mod", "knobs": ["MaxConns","MinConns","ConnConfig.ConnectTimeout"]},
    {"library": "cenkalti/backoff", "source": "go.mod", "knobs": ["MaxRetries","InitialInterval"]}
  ]
}
```

Reading unfamiliar source to find which constant to change is the thing coding agents are already
good at — and it's the reason TortureU targets agents rather than replacing them. Our job is to make
the search small and correct, not to do it. **Zero code for the hardest problem in the design.**

Fallback for humans: the same payload prints as a report. Slower, same information.

---

## Still open

- **Verdict storage format.** Needed before §12 trend tracking is real. Deferred until we have runs
  worth comparing.

---

## Sources

- [k6 2.0 release — AI-assisted testing](https://grafana.com/blog/k6-2-0-release/) · [k6 AI assistant setup](https://grafana.com/docs/k6/latest/set-up/configure-ai-assistant/)
- [Keploy — how it works](https://keploy.io/docs/keploy-explained/how-keploy-works/)
- [Testkube](https://testkube.io/)
- [LitmusChaos](https://litmuschaos.io/) · [Chaos tool comparison (Gremlin)](https://www.gremlin.com/community/tutorials/chaos-engineering-tools-comparison)
- [Steadybit chaos MCP announcement](https://steadybit.com/news/steadybit-launches-the-first-mcp-server-for-chaos-engineering-bringing-experiment-insights-to-llm-workflows/)
- [GoReplay shadow testing](https://goreplay.org/shadow-testing/) · [Speedscale traffic replay guide](https://speedscale.com/blog/definitive-guide-to-traffic-replay/)
- [awesome-deterministic-simulation-testing](https://github.com/ivanyu/awesome-deterministic-simulation-testing) · [Antithesis DST docs](https://antithesis.com/docs/resources/deterministic_simulation_testing/)
- [awesome-chaos-engineering](https://github.com/dastergon/awesome-chaos-engineering)
