package detect

// Dep is an external thing the system under test talks to.
type Dep struct {
	Name    string   // compose service name (or, for lockfile-only types, the type itself)
	Type    string   // normalized: one of the R-DET-9 vocabulary values
	Address string   // host:port, when derivable (R-DET-4)
	Clients []string // R-DET-5 client libraries found in a lockfile
}

// Obs is the observability coverage detected for the system (R-DET-6).
type Obs struct {
	Traces        bool
	Metrics       bool
	Logs          bool
	MaxConfidence string // "caused" if Traces, else "correlated"
}

// Coverage exposes the R-COV-5 facts each registry.yaml predicate namespace
// needs, computed strictly from R-DET-1 inputs (compose + manifests; file
// presence only, no source analysis). has:traffic-capture is intentionally
// absent: it derives from torture.yaml config, which is not an R-DET-1
// input, so internal/detect cannot compute it (reported, not implemented —
// see the Task 1 report).
type Coverage struct {
	OpenAPI   bool // spec:openapi — an OpenAPI/Swagger document exists
	Proto     bool // spec:proto — .proto files exist
	K8s       bool // platform:k8s — Kubernetes manifests or a Helm chart exist
	AWS       bool // platform:aws — AWS SDK found in a manifest
	Azure     bool // platform:azure — Azure SDK found in a manifest
	LacksOtel bool // lacks:otel — no OTel client in any manifest, no collector in compose
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

	// otelClientSeen and otelCollectorSeen feed Coverage.LacksOtel; set by
	// detectLockfiles and the compose service loop respectively, combined
	// once both have run.
	otelClientSeen    bool
	otelCollectorSeen bool
}
