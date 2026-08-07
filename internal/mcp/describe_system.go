package mcp

import "github.com/jdb316/tortureu/internal/detect"

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

// DescribeSystemResult is the describe_system tool's output: services,
// deps, external egress, and observability coverage, plus the gaps
// detection could not classify (R-DET-7).
type DescribeSystemResult struct {
	SUT           string     `json:"sut"`
	Deps          []DepInfo  `json:"deps"`
	Egress        EgressInfo `json:"egress"`
	Observability ObsInfo    `json:"observability"`
	Gaps          []string   `json:"gaps"`
}

// DescribeSystem reports what internal/detect knows about sys, including
// its known-unknowns (R-DET-7): Gaps is always a non-nil slice, so "no
// gaps" and "gaps not reported" are never the same JSON shape.
func DescribeSystem(sys *detect.System) DescribeSystemResult {
	out := DescribeSystemResult{
		Egress: EgressInfo{Classified: []string{}, Unclassified: []string{}},
		Gaps:   []string{},
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
	return out
}
