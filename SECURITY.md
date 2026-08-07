# Security Policy

## Reporting a vulnerability in TortureU

Use GitHub's private vulnerability reporting ("Report a vulnerability" on the Security tab).
Please don't open a public issue first.

Expect an acknowledgement within 7 days.

## Two things worth knowing about what this tool does

**It injects faults.** TortureU degrades and kills things on purpose. Every fault is applied within
container or container-network scope and never to the host (`R-EXE-6`), and faults are torn down on
exit including on crash (`R-EXE-5`). Run it against stacks you own.

**It defaults to denying egress.** External hosts must be classified before a run starts
(`DC-2`). This exists so a 100× replay can't become an outage you cause someone else. Please don't
route around it.

Captured traffic is secret-scrubbed on write, not on replay (`R-DC2-5`), so cassettes are safe to
commit. If you find a case where a credential survives capture, that is a vulnerability — report it.

## Findings we publish about other projects

Our benchmarks run against third-party open-source systems. When we find a real defect in one, we
report it upstream and wait before publishing. See `BENCHMARKS.md`.
