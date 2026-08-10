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

// loadRegistry resolves the registry doctor evaluates against (R-COV-8): an
// explicit path is an opt-in override for testing a modified catalogue;
// otherwise the registry embedded in the binary is used, so `doctor` works
// from any working directory, not only inside this repo.
func loadRegistry(path string) (*doctor.Registry, error) {
	if path != "" {
		return doctor.LoadRegistry(path)
	}
	return doctor.LoadEmbeddedRegistry()
}

// sortByDomainThenID gives a deterministic, stable order to a slice of
// coverage entries: domain, then tool id.
func sortByDomainThenID(entries []doctor.CoverageEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Domain != entries[j].Domain {
			return entries[i].Domain < entries[j].Domain
		}
		return entries[i].Tool.ID < entries[j].Tool.ID
	})
}

// renderPrereqs renders a prerequisite preflight (R-CLI-5: `doctor` MUST
// report whether k6/docker/docker compose are present; escalated as a
// SPEC.md gap by this task before R-CLI-5 existed to describe it).
//
// A tool is reported "found" only on exec.LookPath succeeding — never a
// guessed path — and a missing tool gets an install hint, never a guess at
// where it might be. This is the up-front check that used to be missing
// entirely: previously a user only learned k6 wasn't installed after
// `init` and editing a config, when `run` failed with an adapter error.
func renderPrereqs(checks []PrereqCheck) string {
	var b strings.Builder
	b.WriteString("PREREQUISITES (what this machine can run today)\n")
	for _, c := range checks {
		if c.Found {
			v := c.Version
			if v == "" {
				v = "found"
			}
			fmt.Fprintf(&b, "  [ok] %s: %s\n", c.Name, v)
		} else if c.Required {
			fmt.Fprintf(&b, "  [missing] %s — %s\n", c.Name, c.Hint)
		} else {
			// R-CLI-5: an optional tool's absence is not a problem to fix.
			// "[missing]" reads as a failure and sends the reader to install
			// something `run` never uses.
			fmt.Fprintf(&b, "  [n/a] %s — %s\n", c.Name, c.Hint)
		}
	}
	return b.String()
}

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
// Every delegate- or know-tier line that applies is a suggestion, and every
// suggestion carries both its tier and its trigger condition (the
// registry's `when:`) (R-SCOPE-4, R-CLI-3): doctor is what makes the
// delegate/know tiers reachable (R-SCOPE-3 — one front door to all three
// depths), so it must never let a named-not-executed tool read as
// something tortureu runs today.
//
// The tier/planned/how rendering itself is not reimplemented here: it
// calls doctor.CoverageEntry.String(), the one place that rule is defined
// (R-VER-9's "one document, one renderer" reasoning applied to this
// report). A prior version duplicated that formatting, and a break
// deliberately introduced into internal/doctor's formatter to prove the
// drift passed CI here while failing there — two paths for one rule. Only
// the trigger condition, which String() does not print, is appended
// afterward.
func buildDoctorReport(findings []doctor.Finding, reg *doctor.Registry, sys *detect.System, prereqs []PrereqCheck) string {
	var b strings.Builder

	b.WriteString(renderPrereqs(prereqs))
	b.WriteString("\n")

	// R-CLI-21: whether B1's fidelity numbers cover this platform. Carried
	// here because "macOS is unmeasured" was true and lived only in launch
	// notes no user would ever read.
	b.WriteString("FAULT FIDELITY\n")
	fmt.Fprintf(&b, "  fault magnitude has %s\n", currentFidelityNote())
	b.WriteString("\n")

	// R-CLI-20: what detection DID determine, named plainly. Without this the
	// report never said which service it treated as the system under test, so
	// a wrong guess was invisible and `-service` had no visible effect.
	if sys != nil {
		b.WriteString("DETECTED\n")
		if sys.SUT != "" {
			fmt.Fprintf(&b, "  system under test: %s\n", sys.SUT)
		} else {
			b.WriteString("  system under test: not decided — see GAPS below, and name it with -service\n")
		}
		if sys.Lang != "" {
			fmt.Fprintf(&b, "  language: %s\n", sys.Lang)
		}
		b.WriteString("\n")
	}

	// R-CLI-20 / R-DET-7: what detection could not determine, before the
	// audit that depends on it. doctor referenced Gaps nowhere, so an
	// undecided SUT, an unrecognised image or an unfollowed manifest left
	// the audit quietly empty with no stated reason — while `init` printed
	// every one of them.
	if sys != nil && len(sys.Gaps) > 0 {
		b.WriteString("GAPS (what detection could not determine — R-DET-7)\n")
		for _, g := range sys.Gaps {
			fmt.Fprintf(&b, "  - %s\n", g)
		}
		b.WriteString("\n")
	}

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

	// doctor.Evaluate dereferences sys; a nil System reaches here only from a
	// caller that had nothing to detect, and panicking on it would turn "we
	// could not read your compose file" into a crash.
	if sys == nil {
		sys = &detect.System{}
	}
	entries := doctor.Evaluate(reg, sys)
	domains := map[string]bool{}
	covered := map[string]bool{}
	var driven []doctor.CoverageEntry
	var suggestions []doctor.CoverageEntry
	for _, e := range entries {
		domains[e.Domain] = true
		if e.Applies {
			covered[e.Domain] = true
			switch e.Tool.Tier {
			case "drive":
				driven = append(driven, e)
			case "delegate", "know":
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

	sortByDomainThenID(driven)
	sortByDomainThenID(suggestions)

	// R-SCOPE-3: drive is the tier that is the product (k6, Toxiproxy,
	// stress-ng, pgbench, WireMock, Schemathesis, cgroups, signals — the
	// clock-synchronized execution no other surveyed tool provides
	// off-Kubernetes) and it belongs in its own section, not folded into
	// "SUGGESTIONS": we do not suggest these, we run them. A drive entry
	// whose how: names a verb not yet implemented in v0 still renders via
	// CoverageEntry.String()'s "· planned" / "(verb ... not implemented)"
	// annotation (R-SCOPE-4 in the other direction: claiming we drive
	// something we cannot run today would be the same error inverted).
	b.WriteString("DRIVEN BY TORTUREU (executed on one clock, folded into the verdict)\n")
	if len(driven) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, e := range driven {
		fmt.Fprintf(&b, "  %s   (trigger: %s)\n", e.String(), e.Tool.When)
	}
	b.WriteString("\n")

	b.WriteString("SUGGESTIONS (delegate: config generated and handed off · know: named only — neither executed directly by tortureu; R-SCOPE-4)\n")
	if len(suggestions) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, e := range suggestions {
		fmt.Fprintf(&b, "  %s   (trigger: %s)\n", e.String(), e.Tool.When)
	}

	return b.String()
}

// runDoctor is the `tortureu doctor` verb: resilience audit + registry
// coverage report (R-CLI-1).
func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	compose := fs.String("compose", detect.DefaultComposePath, "path to the compose file to detect")
	service := fs.String("service", "", "name the system under test when R-DET-19 cannot single one out (as `init -service` does)")
	// registryPath default is "" (not "registry.yaml"): the normal path is
	// the registry embedded in the binary (R-COV-8), independent of the
	// working directory, so `doctor` also works everywhere but this repo.
	// A non-empty override is opt-in, for testing a modified catalogue.
	registryPath := fs.String("registry", "", "path to a registry.yaml override; defaults to the registry embedded in the binary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// R-DET-15: an unset -compose resolves by the Compose Specification's
	// own precedence, so a repo using compose.yaml (the canonical name, and
	// what nearly every real project uses) works without a flag.
	composePath, cerr := detect.ResolveComposeArg(*compose)
	if cerr != nil {
		fmt.Fprintf(stderr, "tortureu doctor: %v\n", cerr)
		return 2
	}

	// R-CLI-20: an explicit -service resolves what R-DET-19 refuses to guess.
	// Without it an ambiguous stack was a dead end here: no SUT, an empty
	// audit, and no way for the user to say which service was theirs.
	sys, err := detect.DetectWithSUT(composePath, *service)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu doctor: detect: %v\n", err)
		return 2
	}
	reg, err := loadRegistry(*registryPath)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu doctor: %v\n", err)
		return 2
	}

	findings := doctor.Audit(filepath.Dir(*compose), sys)
	fmt.Fprint(stdout, buildDoctorReport(findings, reg, sys, checkPrerequisites()))
	return 0
}
