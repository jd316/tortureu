package main

// usage is printed for no verb, an unknown verb, or -h/--help at the top
// level. It states what tortureu does, never what it guarantees (R-DC2-7):
// the DC-2 topology enforcement is not yet proven end to end by an
// executable run path, so no "cannot escape" / "cannot reach the internet"
// claim belongs here until that review lands.
const usage = `tortureu is the front door to TortureU's load, fault, and verdict tooling.

Usage:
  tortureu <verb> [flags]

Verbs:
  init      detect the stack, write torture.yaml
  run       execute a scenario, produce a verdict
  smoke     constant-rate sanity check
  doctor    resilience audit + registry coverage report
  mcp       serve the MCP surface
  check     contract compatibility check
  emit      generate a delegate-tier tool's config
  capture   ingest traffic
  replay    replay captured traffic as load

Run "tortureu <verb> -h" for verb-specific flags.
`
