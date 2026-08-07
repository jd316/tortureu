// Package applier provides the two real Applier implementations R-EXE-19
// requires so a passed-over verb is actually routed to its owner instead of
// vanishing: WireMockApplier for error_rate (the mock provider,
// internal/egress's territory) and BrokerApplier for poison_pill and
// duplicate (internal/queuefault's queuefault.Applier seam). Before this
// package, all three verbs were translated and validated but never applied
// — registry.yaml claims `drive` tier for them, so leaving them unimplemented
// would make that claim false and torture.example.yaml (which declares
// error_rate: 0.15) unrunnable (R-EXE-19).
//
// Both appliers talk to their backend over plain HTTP (net/http, already a
// dependency) rather than a client SDK:
//   - WireMockApplier drives WireMock's admin API directly
//     (POST/DELETE /__admin/mappings).
//   - BrokerApplier drives a Kafka REST Proxy-compatible endpoint (Confluent
//     REST Proxy v2 API, which Redpanda's Pandaproxy also implements) rather
//     than a Kafka wire-protocol client library — the smallest thing that
//     produces and consumes messages without adding a new module dependency
//     (go.mod is outside this task's scope).
//
// Teardown honesty (R-EXE-18, mirroring internal/queuefault's package doc):
//   - WireMockApplier's undo is a REAL reversal: it deletes exactly the stub
//     mappings it created, so the mock host returns to whatever behaviour it
//     had before the fault (its base capture/spec stub, untouched). A
//     configured-but-unpublished stub has no durability problem — nothing
//     like a topic log to retract from.
//   - BrokerApplier's undo is NOT a reversal for either verb, because a
//     broker's replicated log has no "un-publish":
//   - ApplyPoisonPill's undo is always a no-op: this implementation
//     produces the whole poison_pill batch synchronously before
//     returning, so by the time undo could run, every malformed message
//     is already durably on the topic. There is nothing left to stop.
//   - ApplyDuplicate's undo stops the background re-delivery loop before
//     its next copy is produced. It does not retract any duplicate
//     already produced, and it does nothing about a duplicate a consumer
//     already read and acted on.
package applier
