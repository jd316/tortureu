# Quickstart — a real verdict in one command

A deliberately broken service, so you can see what TortureU returns before pointing it at
anything of your own. Nothing here is prebuilt: both containers build from source in this
directory.

```sh
cd examples/quickstart
tortureu run
```

You need Docker. You do **not** need k6 — `run` executes it in a pinned container.

## What you should see

```
FAIL  checkout-api  32s

  ✗ http_req_duration: p(95)<1000 -> 3003.87ms
    caused by  dep_slow (dep:9090)  [confidence: correlated]

    look at:  net/http Client.Timeout, Transport.ResponseHeaderTimeout, ...

  ✓ http_req_failed: rate<0.5     0
```

(Exact figures shift a little run to run — the injected stall is 3s, so p95 lands just above it.)
Exit code `1` — an assertion broke. That is a *result*, not an error; `2` would mean TortureU
itself failed (`VERDICT.md` §2).

## The planted defect

`api/main.go` calls its dependency with a zero-value client:

```go
var noTimeoutClient = &http.Client{}   // no Timeout, no deadline anywhere
```

`torture.yaml` injects 300 ms… and then holds the dependency for 3 s during the `peak` phase. With
no request deadline the stall passes straight through to the user, so p95 lands at ~3 s against a
1 s assertion.

The verdict names three things a load test alone cannot: **which fault** (`dep_slow`), **which
dependency** (`dep:9090`), and **which knob** (`Client.Timeout`).

## Things worth trying

- Set `Timeout: 500 * time.Millisecond` on the client in `api/main.go`, then re-run. The assertion
  should pass — you have just proved a fix with the same instrument that found the defect.
- Delete the `faults:` block and re-run. The finding becomes `ambiguous`: with nothing injected
  there is no cause to name, and TortureU will not invent one.
- Add an unclassified external host to `egress:` and re-run. It aborts before load rather than
  letting traffic leave (`DC-2`).

## Why `correlated` and not `caused`

There is no OpenTelemetry here. Attribution is by fault window — one fault was active when the
assertion broke. With traces present TortureU reads the spans and returns `caused` plus a per-hop
chain instead; `evals/corpus/case9-multi-fault-traced` is that case.
