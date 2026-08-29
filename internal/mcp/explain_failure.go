package mcp

import (
	"fmt"

	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/verdict"
)

// knobsByDepType is a small, honest catalogue: library knobs this package
// can actually name for a handful of R-DET-9 dependency types. A type
// absent from this table yields no candidates rather than an invented one
// — the same "never guess" discipline internal/doctor applies to R-AUD-6.
var knobsByDepType = map[string][]string{
	"postgresql": {"MaxConns", "MinConns", "ConnConfig.ConnectTimeout"},
	"mysql":      {"MaxOpenConns", "MaxIdleConns", "ConnMaxLifetime"},
	"redis":      {"PoolSize", "DialTimeout", "ReadTimeout"},
	"mongodb":    {"MaxPoolSize", "ConnectTimeout", "ServerSelectionTimeout"},
	"kafka":      {"DialTimeout", "ReadTimeout", "RetryBackoff"},
	"rabbitmq":   {"Heartbeat", "DialTimeout"},
}

// Explanation is one verdict.Finding expanded into its failure chain,
// stopping at the candidate config surface (R-VER-4, D-9): fault ->
// symptom -> span chain -> candidate library/knobs. It deliberately never
// carries a source location — reading unfamiliar source to pick the exact
// constant to change is the calling agent's job, not this tool's.
type Explanation struct {
	FindingID  string              `json:"finding_id"`
	Fault      string              `json:"fault,omitempty"`
	Symptom    verdict.Broke       `json:"symptom"`
	Chain      []verdict.ChainHop  `json:"chain"`
	Candidates []verdict.Candidate `json:"candidates"`
}

// ExplainFailure expands the finding named findingID from v (R-VER-4).
//
// internal/run does not currently populate Finding.Cause/Chain/Candidates
// (see the task report's spec gap); when a finding does carry them they are
// passed through unmodified, and when it carries a Cause.Target this
// function additionally looks that target up in sys — matching it to a
// detected dependency and, only for dependency types knobsByDepType
// actually names knobs for, filling in one candidate. A target that
// matches nothing, or a dependency type outside the table, yields zero
// candidates: never a guessed one.
func ExplainFailure(v *verdict.Verdict, findingID string, sys *detect.System) (*Explanation, error) {
	if v == nil {
		return nil, fmt.Errorf("mcp: explain_failure: nil verdict")
	}
	for _, f := range v.Findings {
		if f.ID != findingID {
			continue
		}
		ex := &Explanation{
			FindingID:  f.ID,
			Symptom:    f.Broke,
			Chain:      f.Chain,
			Candidates: f.Candidates,
		}
		if f.Cause != nil {
			ex.Fault = f.Cause.Fault
			if ex.Candidates == nil {
				ex.Candidates = candidatesForTarget(f.Cause.Target, sys)
			}
		}
		if ex.Chain == nil {
			ex.Chain = []verdict.ChainHop{}
		}
		if ex.Candidates == nil {
			ex.Candidates = []verdict.Candidate{}
		}
		return ex, nil
	}
	return nil, fmt.Errorf("mcp: explain_failure: verdict %s has no finding %q", v.RunID, findingID)
}

// candidatesForTarget matches target against sys's detected dependencies
// by name or address, and returns a candidate config surface only when
// both a known client library and a knobsByDepType entry exist — never a
// guessed library or a guessed knob list.
func candidatesForTarget(target string, sys *detect.System) []verdict.Candidate {
	if sys == nil {
		return nil
	}
	for _, dep := range sys.Deps {
		if dep.Name != target && dep.Address != target {
			continue
		}
		knobs, ok := knobsByDepType[dep.Type]
		if !ok || len(dep.Clients) == 0 {
			return nil
		}
		return []verdict.Candidate{{Library: dep.Clients[0], Source: "lockfile", Knobs: knobs}}
	}
	return nil
}
