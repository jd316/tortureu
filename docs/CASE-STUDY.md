# Finding a missing timeout, and proving it

Every number here is copied from output committed in [`evals/results/`](../evals/results). Nothing
is illustrative. Re-run it yourself with `make eval`.

## The setup

A Go service calls one dependency. The defect is a single line —
[`examples/quickstart/api/main.go`](../examples/quickstart/api/main.go):

```go
var noTimeoutClient = &http.Client{}   // no Timeout; no deadline anywhere
```

This is not a strawman. A zero-value `http.Client` has no request deadline, `go vet` does not flag
it, and it survives review because it looks like every other client.

A load test finds nothing wrong with this service. It is fast, until the thing it calls is not.

## What a load test tells you

Ramp to 20 rps, hold, assert `p(95) < 1000ms`. Green. The dependency is healthy, so the missing
timeout never costs anything.

The defect is invisible until the dependency is slow — and making it slow *while* the load runs,
on the same clock, is the part that has no off-the-shelf answer outside Kubernetes.

## What TortureU returns

`tortureu run` against the same service, with `latency: 3s` injected at the `peak` phase:

```
FAIL  checkout-api  31s

  ✗ http_req_duration: p(95)<1000 -> 3003.54ms
    caused by  dep_slow (dep:9090)  [confidence: correlated]

    look at:  net/http Client.Timeout, Transport.ResponseHeaderTimeout, ...

  ✓ http_req_failed: rate<0.5     0
```

Three seconds of dependency latency became **3003.54ms of user latency** — passed straight through,
because nothing bounded the request. Note `http_req_failed` is **0**: nothing errored. A failure
that returns no errors is precisely the kind that survives to production.

The verdict names three things the load test could not: the **fault**, the **dependency**, and the
**knob**.

## With traces, it names which of several faults did it

[`case9`](../evals/corpus/case9-multi-fault-traced) runs **two** faults at once — `dep-a` slowed,
`dep-b` down — against an OpenTelemetry-instrumented stack:

```
✗ http_req_duration: p(95)<500 -> 3004.23ms
  caused by dep_a_slow   [confidence: caused]

  dep-a:9091                latency  883µs -> 3002.9ms   (n=200 spans)
  checkout-api  POST /checkout       2.1ms -> 3003.7ms   (n=200 spans)
```

It read the spans, found that only `dep-a`'s target actually degraded, and named it. If **two**
targets degrade, or none do, it stays `ambiguous` and names nothing — a guess is worth less than
silence here.

## The results that matter most

There are two controls, and they prove different things.

**Case 8** — same harness, same load, **no planted defect and no fault**:

```
case8-control:pass:0        →  0 findings
```

**Case 10** — a **real** 3s dependency stall is injected, and the service survives it, because it
has a 500 ms deadline and a degraded-but-valid fallback:

```
PASS  checkout-api

  ✓ http_req_duration: p(95)<1500     501.096ms
  ✓ http_req_failed: rate<0.5         0
```

`501.096ms` is the timeout doing its job. This is the harder guarantee: reporting a finding here
would mean reporting **the fault we injected** rather than a defect in the service — the subtlest
way an attribution tool can be wrong, and the one you cannot catch by testing only broken things.

A tool that always finds something is a random number generator with good typography. These two
cases are the reason to believe the other eight.

## What it does not do

- **Attribution is 5/5** on findings from runs that injected a fault, and **5/9** across every
  finding. Three corpus cases inject nothing at all; those stay `ambiguous`, because with no fault
  there is no cause to name.
- **`caused` requires traces.** Without OpenTelemetry a verdict tops out at `correlated` and shows
  no chain, because a chain that was not measured would have to be invented.
- **It names a knob, never a `file:line`.** `Client.Timeout` is the surface; finding the constant
  is yours.
- **Fault fidelity is measured on Linux only.** `tortureu doctor` says so on other platforms
  rather than letting you assume otherwise.

## Reproduce it

```sh
cd examples/quickstart && tortureu run     # the case above, ~30s, Docker only
make eval                                  # the whole labelled corpus, including the control
```
