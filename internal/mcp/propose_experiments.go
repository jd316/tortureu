package mcp

import (
	"fmt"
	"sort"

	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/doctor"
)

// ExperimentProposal is one ranked, ready-to-paste torture.yaml faults:
// fragment (R-MCP-4: a fragment, not prose). Lower Rank means higher
// priority.
type ExperimentProposal struct {
	Name     string `json:"name"`
	Rank     int    `json:"rank"`
	Fragment string `json:"fragment"`
}

// faultVerbForCheck maps a doctor.Check to the SPEC.md §4.4 inject verb
// (and a conservative default value) that would exercise it — the same
// association doctor.experimentFor already names in its Finding.Experiment
// prose, expressed here as structured inject: content instead.
func faultVerbForCheck(check doctor.Check) (verb string, inject string) {
	switch check {
	case doctor.CheckRetry:
		return "down", "down: true"
	default: // doctor.CheckTimeout
		return "latency", "latency: 300ms, jitter: 50ms"
	}
}

// rankFor orders findings by how much a run would settle: a confirmed
// static gap (Determined && !Present) ranks first because a run can prove
// exactly that gap; a not-yet-determined finding ranks next since it is
// still entirely unproven; a finding where static inspection already found
// evidence ranks last — still worth the dynamic proof (doctor's own
// "static hint, dynamic proof" framing), just the lowest-yield of the
// three.
func rankFor(f doctor.Finding) int {
	switch {
	case f.Determined && !f.Present:
		return 1
	case !f.Determined:
		return 2
	default:
		return 3
	}
}

// fragmentFor renders f into a torture.yaml faults: fragment (R-MCP-4). The
// anchor uses the t=<duration> grammar (R-CFG-11) rather than a phase name:
// this package has no torture.yaml load: block to anchor against, only the
// detected topology, so an absolute-time anchor is the only one it can
// name without guessing at the caller's own load profile.
func fragmentFor(f doctor.Finding) string {
	_, inject := faultVerbForCheck(f.Check)
	return fmt.Sprintf(
		"faults:\n  - name: %s_%s\n    at: t=30s\n    for: 30s\n    target: %s\n    inject: { %s }\n",
		f.DepName, f.Check, f.DepName, inject,
	)
}

// ProposeExperiments ranks experiment fragments for the detected topology
// (R-MCP-4), driven by doctor's static resilience audit (R-AUD-1..4): each
// audit finding becomes one fault fragment targeting the dependency it was
// raised against, ordered by rankFor.
func ProposeExperiments(dir string, sys *detect.System) []ExperimentProposal {
	if sys == nil {
		return nil
	}
	findings := doctor.Audit(dir, sys)
	proposals := make([]ExperimentProposal, 0, len(findings))
	for _, f := range findings {
		proposals = append(proposals, ExperimentProposal{
			Name:     fmt.Sprintf("%s_%s", f.DepName, f.Check),
			Rank:     rankFor(f),
			Fragment: fragmentFor(f),
		})
	}
	sort.SliceStable(proposals, func(i, j int) bool { return proposals[i].Rank < proposals[j].Rank })
	return proposals
}
