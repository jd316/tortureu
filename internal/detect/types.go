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

// System is what detection knows about a repo.
type System struct {
	SUT    string // compose service name of the system under test (R-DET-8)
	Deps   []Dep
	Egress []string // external hosts found (R-DET-4)
	Obs    Obs
	Gaps   []string // things we could not classify; reported, never guessed (R-DET-3, R-DET-7)
	Lang   string   // detected from manifest
}
