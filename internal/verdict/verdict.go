// Package verdict defines the normalized verdict document (VERDICT.md §1):
// the one shape a TortureU run emits, that both machine (JSON) and human
// consumers read. See SPEC.md §6 (R-VER-1..R-VER-9) for the requirements this
// package implements; R-VER-10 (k6 JSON ingestion) belongs to internal/k6.
package verdict

import "encoding/json"

// Status is the outcome of a run. status:error (TortureU itself broke) and
// status:fail (the system under test broke an assertion) are distinct values
// on purpose — R-VER-2 forbids conflating them.
type Status string

const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusError   Status = "error"
	StatusAborted Status = "aborted"
)

// Confidence is assigned per-finding, not per-run (R-VER-3, D-4).
type Confidence string

const (
	// Caused requires traces spanning the fault window.
	Caused Confidence = "caused"
	// Correlated requires exactly one fault active in the breach window.
	Correlated Confidence = "correlated"
	// Ambiguous means >=2 candidate causes and no traces.
	Ambiguous Confidence = "ambiguous"
)

// Broke describes the assertion a finding is about: what broke, what was
// observed, and when.
type Broke struct {
	Assertion  string `json:"assertion"`
	Observed   string `json:"observed"`
	At         string `json:"at"`
	SustainedS int    `json:"sustained_s,omitempty"`
}

// Cause describes the fault believed (or known) to have caused a finding.
type Cause struct {
	Fault  string         `json:"fault"`
	Target string         `json:"target"`
	Inject map[string]any `json:"inject,omitempty"`
	Window []string       `json:"window,omitempty"`
}

// ChainHop is one fault -> symptom hop in a finding's causal chain.
type ChainHop struct {
	At       string `json:"at"`
	Observed string `json:"observed"`
}

// Candidate names a candidate config surface (library + knobs) to look at.
// Deliberately has no source-location field: R-VER-4 forbids reporting a
// file:line — the last mile is the agent's (D-9).
type Candidate struct {
	Library string   `json:"library"`
	Source  string   `json:"source"`
	Knobs   []string `json:"knobs"`
}

// Finding is one entry in a verdict's findings list: why the run failed.
type Finding struct {
	ID            string      `json:"id"`
	Confidence    Confidence  `json:"confidence"`
	Broke         Broke       `json:"broke"`
	Cause         *Cause      `json:"cause,omitempty"`
	Chain         []ChainHop  `json:"chain,omitempty"`
	Candidates    []Candidate `json:"candidates,omitempty"`
	Amplification string      `json:"amplification,omitempty"`
}

// Passed is one assertion that held. Its presence proves the assertion ran,
// as distinct from having been skipped (R-VER-5).
type Passed struct {
	Assertion string `json:"assertion"`
	Observed  string `json:"observed"`
}

// EgressAudit is the DC-2 proof: every host the run touched, classified
// (R-VER-6).
type EgressAudit struct {
	Mocked       []string `json:"mocked"`
	Blocked      []string `json:"blocked"`
	Real         []string `json:"real"`
	Unclassified []string `json:"unclassified"`
}

// Observability records what this run's setup could actually support.
type Observability struct {
	Traces        bool       `json:"traces"`
	Metrics       bool       `json:"metrics"`
	Logs          bool       `json:"logs"`
	MaxConfidence Confidence `json:"max_confidence,omitempty"`
}

// Verdict is the one document a run emits (R-VER-1). The same value is
// marshaled to JSON for machine consumers and passed to Render for humans
// (R-VER-9) — there is no second, independently-maintained representation.
type Verdict struct {
	RunID     string `json:"run_id"`
	Scenario  string `json:"scenario"`
	Status    Status `json:"status"`
	StartedAt string `json:"started_at"`
	DurationS int    `json:"duration_s"`
	Commit    string `json:"commit,omitempty"`
	Reset     string `json:"reset,omitempty"`
	// Error is why status=error: what the tool itself broke on, e.g.
	// "k6 not found on PATH" or "docker compose build: no Dockerfile for
	// service checkout-api". MUST be set whenever Status == StatusError
	// (R-VER-2) — an error with no reason is indistinguishable from a shrug.
	// Say what failed and, where knowable, what to do about it: "k6 not
	// found on PATH" is actionable, "run failed" is not.
	Error         string         `json:"error,omitempty"`
	Findings      []Finding      `json:"findings"`
	Passed        []Passed       `json:"passed"`
	EgressAudit   EgressAudit    `json:"egress_audit"`
	Observability Observability  `json:"observability"`
	Metrics       map[string]any `json:"metrics,omitempty"`
	Artifacts     map[string]any `json:"artifacts,omitempty"`
}

// verdictAlias lets MarshalJSON default nil slices to `[]` without infinite
// recursion through Verdict's own MarshalJSON.
type verdictAlias Verdict

// MarshalJSON ensures findings/passed/egress lists always render as JSON
// arrays, never `null` — an absent vs. empty distinction agents should never
// have to make (R-VER-5, R-VER-6).
func (v Verdict) MarshalJSON() ([]byte, error) {
	a := verdictAlias(v)
	if a.Findings == nil {
		a.Findings = []Finding{}
	}
	if a.Passed == nil {
		a.Passed = []Passed{}
	}
	if a.EgressAudit.Mocked == nil {
		a.EgressAudit.Mocked = []string{}
	}
	if a.EgressAudit.Blocked == nil {
		a.EgressAudit.Blocked = []string{}
	}
	if a.EgressAudit.Real == nil {
		a.EgressAudit.Real = []string{}
	}
	if a.EgressAudit.Unclassified == nil {
		a.EgressAudit.Unclassified = []string{}
	}
	return json.Marshal(a)
}
