# Contributing to TortureU

This repo enforces more than the usual `go test` on a pull request, and the extra rules exist for
one reason: **TortureU's output is evidence a developer will act on.** A claim it cannot support is
worse than no claim, so the process is built to make unsupported claims fail loudly rather than
merge quietly.

Read this before your first PR — several of these are checked mechanically and the failure messages
assume you have.

## The gate

CI runs exactly this, and so should you:

```sh
go build ./...
go vet ./...
gofmt -l .          # must print nothing
go test ./... -race
python3 check.py    # docs, spec and registry must agree
```

`check.py` is not a linter. It fails the build when the documentation and the code disagree — a
registry entry naming a verb or flag that does not exist, a test citing a requirement id that was
never written, a stated tool count that no longer matches `registry.yaml`, a doc linking to
benchmark evidence that is not committed.

## Spec first, then a failing test, then code

Three rules, in this order, from `SPEC.md` §1:

1. **R-PROC-2 — behaviour is specified before it is tested.** If what you are building is not in
   `SPEC.md`, amend `SPEC.md` first. Never invent behaviour in code. A genuine unknown becomes a
   `TBD-n` in SPEC §12 rather than a guess in a function.
2. **R-PROC-1 — no production code without a failing test.** Write it, watch it fail for the right
   reason, then make it pass.
3. **R-PROC-3 — every test cites the requirement it proves**, as `// spec: R-XXX-n`. `check.py`
   fails on an id that does not exist in `SPEC.md`.

New requirements take the next free id and are marked `*(proposed)*`. If you add one, update
README's stated requirement count — `check.py` checks it.

## Never claim verification you did not perform

This is the rule the project cares about most.

- If you could not run something against the real tool, say so **in the file header**, in a
  `VERIFICATION STATUS:` block. Several emitters carry one. "Not verified, and here is why" is a
  perfectly good outcome; "verified" when you did not is not.
- Prefer running the real binary over asserting what you believe it does. Most defects found in
  this codebase came from executing something, not from reading it: `oasdiff` exits 0 when it finds
  breaking changes, `buf` exits 100, `ghz` panics on `--load-start=0`, LocalStack `:latest` exits 55
  on a licence check. None of that is discoverable by inspection.
- A test that skips is honest; a test that passes without exercising anything is not.

## Prove a new check can fail

When you add a gate — to `check.py`, to CI, to a harness — **break the thing it guards, watch the
check fail, then restore it.** Say so in the commit message.

This is not ceremony. A gate that cannot fail is worse than no gate, because it reads as coverage.
Real examples from this repo's history: a meta-gate that would have passed the exact input it
existed to reject, a tier-count check not bound to its tier, and an eval launch gate that certified
a corpus in which every case aborted.

## Never let a failure be silent

Refuse and explain rather than guessing or quietly doing nothing.

- Do not guess a host, port, credential, database name, topic or language. Refuse, and say what you
  needed. `noDepNote` in `internal/emit` is the established shape.
- Distinguish "I could not run the check" from "the check passed". A tool error is exit 2; a real
  finding about the user's service is exit 1 (`VERDICT.md` §2, R-VER-2). Reporting a finding as a
  tool error sends someone to debug TortureU instead of their system.
- If an emitter cannot translate part of a `torture.yaml`, it must say so per item. Silent omission
  is the failure mode this project rejects everywhere.

## Tiers, and the noun rule

`registry.yaml` classifies every tool as `drive` (we co-execute it on our clock), `delegate` (we
generate its config and hand off) or `know` (we name it with a trigger condition). Claiming a
higher tier than the code delivers is a documentation bug and `check.py` will catch part of it.

**DC-1:** TortureU never exposes a tool named with a k6 noun (`script`, `test`, `threshold`), with
the single sanctioned exception of `emit_k6_script`.

**R-LIC-1:** k6 is AGPL-3 and this project is MIT. We generate k6 scripts as text and never link,
vendor or redistribute k6. Do not add a dependency that would change that.

## Commits and scope

- Touch the package your change belongs to. Do not edit an unrelated one to make yours build.
- Commit with explicit paths (`git commit -- <paths>`), never `git add -A`. Note that
  `git commit -- <path>` does **not** stage an untracked file, and `git add` silently does nothing
  on a gitignored path — both have shipped incomplete commits here.
- Explain *why* in the commit message. What changed is in the diff.

## Running the evidence

```sh
make bench   # B1 fault fidelity + B2 harness overhead — needs Docker
make eval    # E1 attribution accuracy against the labelled corpus — needs Docker
```

`make eval` refuses to score a corpus in which any case aborted, and requires the control case
(case 8, no planted defect) to pass with zero findings. If you add a corpus case, add it because we
got something wrong in the wild — never trim one because it is inconvenient.
