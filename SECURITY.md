# Security Policy

## Reporting a vulnerability in TortureU

Use GitHub's private vulnerability reporting ("Report a vulnerability" on the Security tab).
Please don't open a public issue first.

Expect an acknowledgement within 7 days.

## Two things worth knowing about what this tool does

**It injects faults.** TortureU degrades and kills things on purpose. Every fault is applied within
container or container-network scope and never to the host (`R-EXE-6`). Run it against stacks you own.

Faults are torn down on normal exit, on panic, and on SIGINT/SIGTERM (`R-EXE-5`, `R-EXE-16`).
**They are not torn down on SIGKILL**, which no process can trap — a `kill -9`'d run leaves its
faults applied until you remove them. Queue faults are narrower still: teardown stops further
injection but cannot un-publish a poison pill already in a topic, nor retract a delivered duplicate
(`R-EXE-18`). We state these limits rather than imply protection we cannot deliver.

**It defaults to denying egress.** A run's stack is placed on an `internal: true` Docker network
with no route out, and reaches classified hosts only through a proxy (`DC-2`, `R-DC2-3`). That
isolation is enforced topologically and proven end to end by a real-Docker test with a negative
control. It exists so a 100× replay can't become an outage you cause someone else. Please don't
route around it.

**Capture is not implemented in v0**, so `R-DC2-5`'s secret-scrubbing has nothing to attach to yet
(`TBD-8`). There are no cassettes, and nothing here should be read as a claim that captured traffic
is currently scrubbed. Scrubbing is required to ship in the same change as capture, never after —
retrofitting it onto an existing corpus means the unscrubbed cassettes already exist.

## Findings we publish about other projects

If our benchmarks ever run against third-party open-source systems and we find a real defect, we
report it upstream and wait before publishing. See `BENCHMARKS.md` — note that no benchmark has
been run yet.
