<!-- CONTRIBUTING.md has the full rules; this is the short version. -->

## What this changes, and why

<!-- The diff shows what. Explain why. -->

## Checklist

- [ ] Behaviour is specified in `SPEC.md` **before** it was tested (R-PROC-2). New requirements take
      the next free id and are marked `*(proposed)*`
- [ ] A test was written first and watched fail for the right reason (R-PROC-1)
- [ ] Every test cites its requirement as `// spec: R-XXX-n` (R-PROC-3)
- [ ] `go build ./... && go vet ./... && gofmt -l . && go test ./... -race && python3 check.py`
- [ ] If this adds a gate: the thing it guards was broken, the gate was watched failing, then
      restored — and the PR says so. A gate that cannot fail reads as coverage and is worse than none

## Verification

<!-- What you ran, and what it printed. If you could not verify something, say so plainly and say
     why — "not verified, because X" is a fine answer here. Claiming verification you did not
     perform is the one thing this project treats as unacceptable. -->
