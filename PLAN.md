# TortureU v0 — Implementation Plan

Source of truth is `SPEC.md`. Each task cites the requirements it must satisfy.

## Global Constraints

- **TDD (R-PROC-1):** no production code without a failing test first. Tests carry
  `// spec: R-XXX-n` (R-PROC-3); `python3 check.py` fails on an unknown id.
- **SDD (R-PROC-2):** if behaviour isn't in SPEC.md, amend SPEC.md before testing it.
  Never invent behaviour in code. Unknowns go to SPEC §12 as `TBD-n` (R-PROC-4).
- **Minimal (YAGNI):** the least code that passes. No speculative abstraction, no
  interface with one implementation, no config for a constant.
- **Gate:** `go test ./... -race`, `go vet ./...`, `gofmt -l .` empty, `python3 check.py`.
- **Scope:** touch only your own package. Do not edit another task's directory.

## Task 1 — internal/detect (GREEN an existing RED suite)

12 failing tests already exist in `internal/detect/*_test.go`. Make them pass.
Requirements: R-DET-1..R-DET-11. Use `compose-spec/compose-go/v2` (R-DET-11).
Do not weaken or rewrite the tests to fit the implementation.

## Task 2 — internal/config (torture.yaml)

Parse and validate `torture.yaml`. Requirements: R-CFG-1..R-CFG-21.
Reference document: `torture.example.yaml` must parse and validate clean.
Notable: unknown top-level keys are an error (R-CFG-2); empty `assert` is an error
(R-CFG-19); exactly one inject verb per fault (R-CFG-14); `at:` grammar and undeclared
phase is an error (R-CFG-11, R-CFG-12); open model only (R-CFG-6).

## Task 3 — internal/verdict

Verdict document, confidence, exit codes, human rendering.
Requirements: R-VER-1..R-VER-10. `VERDICT.md` §1 is normative for field names,
§2 for exit codes, §4 for the human rendering. Same document renders both (R-VER-9).

## Task 4 — internal/k6

Compile the load block to a k6 script; model its machine-readable output.
R-CFG-6..9, R-EXE-8 (stage-transition markers — the "one clock" mechanism),
R-EXE-9 (no remote jslib fetch), R-VER-10, R-LIC-1 (generate text; never link k6).

## Task 5 — internal/fault

Fault verbs -> Toxiproxy toxics and Docker actions. R-CFG-14, R-EXE-5 (teardown on
panic/abort), R-EXE-6 (container scope only — portability and never degrade the host),
R-EXE-7.

## Task 6 — internal/egress

DC-2 default-deny, enforced topologically (`internal: true` network + dual-homed proxy).
R-DC2-1..5, R-DET-4, R-VER-6. Unclassified host aborts before load, exit 3.

## Task 7 — internal/run

The orchestrator, and the product's core claim: load and faults on one clock.
R-SCOPE-2, R-EXE-1..9, R-CFG-20/21, R-VER-1..3.

## Task 8 — cmd/tortureu

Nine verbs; `init` and `run` real, the rest exit 2 with "not implemented in v0".
Stdlib `flag` only. R-CLI-1..3, R-VER-7..9, R-SCOPE-4, R-DC1-3/4.

## Task 9 — internal/doctor + internal/mcp

Resilience audit (hints, never failures) + registry coverage; five MCP tools obeying
the noun rule. R-AUD-1..5, R-MCP-1..5, R-DC1-1/2, R-COV-1..4.

## Dependency order

```
1 detect ─┐
2 config ─┼─> 4 k6 ──┐
3 verdict─┘   5 fault ├─> 7 run ─> 8 cli ─> 9 doctor+mcp
              6 egress┘
```
Batches 1 (T1-3) and 2 (T4-6) parallelise; T7-9 are sequential.
