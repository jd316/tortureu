package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/doctor"
)

// buildDoctorReport renders the resilience audit and registry coverage
// (R-CLI-3) as one human-readable report.
//
// Resilience findings are always printed as hints (R-AUD-3) — doctor never
// fails a build.
//
// A domain is "uncovered" when no registry tool matched the detected
// system at all (SPEC.md does not further subdivide "uncovered", so this is
// the literal reading: nothing in the registry currently applies).
//
// Every know-tier line that applies is a suggestion, and every suggestion
// carries both its tier and its trigger condition (the registry's `when:`)
// (R-SCOPE-4, R-CLI-3): doctor is what makes the delegate/know tiers
// reachable, so it must never let a named-not-executed tool read as
// something tortureu runs.
func buildDoctorReport(findings []doctor.Finding, reg *doctor.Registry, sys *detect.System) string {
	var b strings.Builder

	b.WriteString("RESILIENCE AUDIT (hints only, R-AUD-3)\n")
	if len(findings) == 0 {
		b.WriteString("  (no known client libraries detected)\n")
	}
	for _, f := range findings {
		fmt.Fprintf(&b, "  hint: %s (%s): %s\n", f.DepName, f.Check, f.Hint)
		if f.Experiment != "" {
			fmt.Fprintf(&b, "        %s\n", f.Experiment)
		}
	}
	b.WriteString("\n")

	entries := doctor.Evaluate(reg, sys)
	domains := map[string]bool{}
	covered := map[string]bool{}
	var suggestions []doctor.CoverageEntry
	for _, e := range entries {
		domains[e.Domain] = true
		if e.Applies {
			covered[e.Domain] = true
			if e.Tool.Tier == "know" {
				suggestions = append(suggestions, e)
			}
		}
	}

	var uncovered []string
	for d := range domains {
		if !covered[d] {
			uncovered = append(uncovered, d)
		}
	}
	sort.Strings(uncovered)

	fmt.Fprintf(&b, "REGISTRY COVERAGE (%d domains, %d tools)\n", reg.DomainCount(), reg.ToolCount())
	if len(uncovered) == 0 {
		b.WriteString("  uncovered domains: none\n")
	} else {
		fmt.Fprintf(&b, "  uncovered domains: %s\n", strings.Join(uncovered, ", "))
	}
	b.WriteString("\n")

	sort.Slice(suggestions, func(i, j int) bool {
		if suggestions[i].Domain != suggestions[j].Domain {
			return suggestions[i].Domain < suggestions[j].Domain
		}
		return suggestions[i].Tool.ID < suggestions[j].Tool.ID
	})

	b.WriteString("SUGGESTIONS (know-tier — named, not executed; R-SCOPE-4)\n")
	if len(suggestions) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, e := range suggestions {
		fmt.Fprintf(&b, "  [%s] %s/%s: %s   (trigger: %s)\n", e.Tool.Tier, e.Domain, e.Tool.ID, e.Tool.How, e.Tool.When)
	}

	return b.String()
}

// runDoctor is the `tortureu doctor` verb: resilience audit + registry
// coverage report (R-CLI-1).
func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	compose := fs.String("compose", "docker-compose.yml", "path to the compose file to detect")
	registryPath := fs.String("registry", "registry.yaml", "path to registry.yaml")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	sys, err := detect.Detect(*compose)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu doctor: detect: %v\n", err)
		return 2
	}
	reg, err := doctor.LoadRegistry(*registryPath)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu doctor: %v\n", err)
		return 2
	}

	findings := doctor.Audit(filepath.Dir(*compose), sys)
	fmt.Fprint(stdout, buildDoctorReport(findings, reg, sys))
	return 0
}
