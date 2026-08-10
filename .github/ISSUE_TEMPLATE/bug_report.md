---
name: Bug report
about: Something TortureU did that it should not have, or did not do that it should
labels: bug
---

## What happened

<!-- Paste the verdict or the error. The exit code matters: 1 is a result about your service,
     2 means TortureU itself failed. -->

```
```

## What you expected

## Reproducing it

- [ ] `tortureu doctor` output (it reports what this machine can do, what detection found, and any
      gaps — please paste it, it answers most of the questions we would ask)
- [ ] Your `torture.yaml` (redact hosts/credentials — `capture` scrubs them, issue text does not)
- [ ] The relevant part of your compose file

## Environment

- TortureU version / commit:
- OS and Docker version:
- Docker Desktop, Colima, Engine, or something else:

<!-- Fault *fidelity* is measured on Linux/cgroup v2 only. `doctor` says so on other platforms.
     If a fault's magnitude looks wrong on macOS or Windows, say which platform — that is a known
     unmeasured area, not a surprise, and a report there is genuinely useful. -->
