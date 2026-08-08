package detect

// Dep is an external thing the system under test talks to.
type Dep struct {
	Name       string      // compose service name (or, for lockfile-only types, the type itself)
	Type       string      // normalized: one of the R-DET-9 vocabulary values
	Address    string      // host:port, when derivable (R-DET-4)
	Clients    []string    // R-DET-5 client libraries found in a lockfile
	ClientRefs []ClientRef // Clients, each attributed to its originating service
}

// ClientRef is one client library import (R-DET-5), together with the
// compose service whose manifest it was read from. Manifests are read only
// from the compose-project root and each service's own declared build
// context (R-DET-1: a bounded, compose-declared location, never a general
// tree walk). Service is "" when the import came from the project root and
// no service's build context is that same directory.
type ClientRef struct {
	Import  string
	Service string
}

// Obs is the observability coverage detected for the system (R-DET-6).
type Obs struct {
	Traces        bool
	Metrics       bool
	Logs          bool
	MaxConfidence string // "caused" if Traces, else "correlated" (never "" — R-DET-6, TBD-6)
}

// Fact is a tri-state predicate value (R-COV-6). A plain bool can only say
// true/false, which conflates "verified absent" with "we couldn't check" —
// exactly the silent-failure mode R-COV-6 forbids. The zero value is
// FactUnknown, so an un-set Fact defaults to the safe "undetermined" state
// rather than silently reading as false.
type Fact int

const (
	FactUnknown Fact = iota
	FactTrue
	FactFalse
)

func (f Fact) String() string {
	switch f {
	case FactTrue:
		return "true"
	case FactFalse:
		return "false"
	default:
		return "unknown"
	}
}

// Coverage exposes the R-COV-5 facts each registry.yaml predicate namespace
// needs, computed strictly from R-DET-1 inputs (compose + manifests; file
// presence only, no source analysis). has:traffic-capture is intentionally
// absent: it derives from torture.yaml config, which is not an R-DET-1
// input, so internal/detect cannot compute it (reported, not implemented —
// see the Task 1 report).
//
// OpenAPI/Proto/K8s stay plain bool: they are pure file-presence checks that
// always run regardless of which manifest (if any) is present, so they are
// never in an undetermined state. AWS/Azure/LacksOtel depend on a manifest
// whose declared dependencies might not all be readable (R-DET-14) — when
// the manifest points at sources outside what R-DET-1 permits reading (a
// Maven aggregator pom's modules), those three MUST report FactUnknown,
// never FactFalse (R-COV-6).
type Coverage struct {
	OpenAPI   bool // spec:openapi — an OpenAPI/Swagger document exists
	Proto     bool // spec:proto — .proto files exist
	K8s       bool // platform:k8s — Kubernetes manifests or a Helm chart exist
	AWS       Fact // platform:aws — AWS SDK found in a manifest
	Azure     Fact // platform:azure — Azure SDK found in a manifest
	LacksOtel Fact // lacks:otel — no OTel client in any manifest, no collector in compose
}

// System is what detection knows about a repo.
type System struct {
	SUT         string // compose service name of the system under test (R-DET-8)
	Deps        []Dep
	Egress      []string          // external hosts found (R-DET-4)
	EgressClass map[string]string // Egress entry -> "internal" (in-compose) or "unclassified" (R-DET-4)
	Obs         Obs
	Coverage    Coverage // R-COV-5 predicate-namespace facts
	Gaps        []string // things we could not classify; reported, never guessed (R-DET-3, R-DET-7)
	Lang        string   // detected from manifest

	// otelClientSeen, otelClientUnknown and otelCollectorSeen feed
	// Coverage.LacksOtel; set by detectLockfiles and the compose service
	// loop respectively, combined once both have run.
	otelClientSeen    bool
	otelClientUnknown bool
	otelCollectorSeen bool
}
