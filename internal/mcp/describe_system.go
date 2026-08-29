package mcp

import (
	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/doctor"
)

// DepInfo is one detected dependency, mirrored from detect.Dep for the
// describe_system tool's output shape.
type DepInfo struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Address string   `json:"address,omitempty"`
	Clients []string `json:"clients,omitempty"`
}

// EgressInfo classifies the hosts detect.Detect found reachable from the
// stack. Classified means detection could place it (currently only
// "internal": an in-compose service); Unclassified is exactly the set
// R-DC2-2's abort would name.
type EgressInfo struct {
	Classified   []string `json:"classified"`
	Unclassified []string `json:"unclassified"`
}

// ObsInfo is the observability coverage a run against this system could
// actually support.
type ObsInfo struct {
	Traces        bool   `json:"traces"`
	Metrics       bool   `json:"metrics"`
	Logs          bool   `json:"logs"`
	MaxConfidence string `json:"max_confidence,omitempty"`
}

// Suggestion is one registry.yaml tool applicable to the detected system
// (R-MCP-6): the delegate/know-tier reach the five MCP tools alone don't
// have. Tier is always carried (R-SCOPE-4) — an agent reading Suggestions
// must never mistake a `know`-tier name for something this surface can
// execute; only `run_experiment` executes anything (R-MCP-2).
type Suggestion struct {
	Domain string `json:"domain"`
	ID     string `json:"id"`
	Tier   string `json:"tier"`
	How    string `json:"how"`
	Note   string `json:"note,omitempty"`
}

// DescribeSystemResult is the describe_system tool's output: services,
// deps, external egress, observability coverage, registry suggestions
// (R-MCP-6), plus the gaps detection could not classify (R-DET-7).
type DescribeSystemResult struct {
	SUT           string       `json:"sut"`
	Deps          []DepInfo    `json:"deps"`
	Egress        EgressInfo   `json:"egress"`
	Observability ObsInfo      `json:"observability"`
	Suggestions   []Suggestion `json:"suggestions"`
	Gaps          []string     `json:"gaps"`
}

// DescribeSystem reports what internal/detect knows about sys, including
// its known-unknowns (R-DET-7): Gaps is always a non-nil slice, so "no
// gaps" and "gaps not reported" are never the same JSON shape.
func DescribeSystem(sys *detect.System) DescribeSystemResult {
	out := DescribeSystemResult{
		Egress:      EgressInfo{Classified: []string{}, Unclassified: []string{}},
		Suggestions: []Suggestion{},
		Gaps:        []string{},
	}
	if sys == nil {
		return out
	}

	out.SUT = sys.SUT
	for _, d := range sys.Deps {
		out.Deps = append(out.Deps, DepInfo{
			Name:    d.Name,
			Type:    d.Type,
			Address: d.Address,
			Clients: d.Clients,
		})
	}

	for _, host := range sys.Egress {
		if sys.EgressClass[host] == "unclassified" || sys.EgressClass[host] == "" {
			out.Egress.Unclassified = append(out.Egress.Unclassified, host)
		} else {
			out.Egress.Classified = append(out.Egress.Classified, host)
		}
	}

	out.Observability = ObsInfo{
		Traces:        sys.Obs.Traces,
		Metrics:       sys.Obs.Metrics,
		Logs:          sys.Obs.Logs,
		MaxConfidence: sys.Obs.MaxConfidence,
	}

	if sys.Gaps != nil {
		out.Gaps = append(out.Gaps, sys.Gaps...)
	}

	out.Suggestions = suggestionsFor(sys)
	return out
}

// suggestionsFor evaluates the embedded registry.yaml against sys (R-MCP-6),
// reusing internal/doctor's own evaluator rather than reimplementing
// predicate matching. Only entries doctor could actually evaluate and that
// matched are surfaced — an unevaluable predicate is a gap doctor already
// declines to guess at (R-COV-6), not a suggestion. A registry load failure
// (never expected against the embedded copy) degrades to no suggestions
// rather than a panic or a fabricated one.
func suggestionsFor(sys *detect.System) []Suggestion {
	reg, err := doctor.LoadEmbeddedRegistry()
	if err != nil {
		return []Suggestion{}
	}
	out := []Suggestion{}
	for _, entry := range doctor.Evaluate(reg, sys) {
		if !entry.Evaluated || !entry.Applies {
			continue
		}
		out = append(out, Suggestion{
			Domain: entry.Domain,
			ID:     entry.Tool.ID,
			Tier:   entry.Tool.Tier,
			How:    entry.Tool.How,
			Note:   entry.Tool.Note,
		})
	}
	return out
}
