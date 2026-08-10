---
name: Wrong or missing finding
about: The verdict blamed the wrong thing, invented a finding, or missed a real one
labels: attribution
---

<!-- This is the report we most want. The project's central claim is that a named cause is
     trustworthy, so a wrong one is the most serious kind of bug here — more serious than a crash. -->

## Which way was it wrong

- [ ] It **invented** a finding (nothing was actually broken)
- [ ] It **blamed the wrong** dependency or fault
- [ ] It **missed** a defect that was really there
- [ ] It said `caused` when it should not have (or vice versa)

## The verdict

```
```

## What was actually true

<!-- What the real cause was, and how you know. -->

## Can it become a corpus case?

The labelled corpus in `evals/corpus/` exists to catch exactly this, and cases are added when we
get something wrong in the wild — never trimmed when inconvenient. If you can share a minimal
compose stack that reproduces it, say so; a reproducible wrong answer is worth more to this project
than a feature request.
