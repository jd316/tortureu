package verdict

import (
	"fmt"
	"strings"
)

// Render formats a Verdict for humans (VERDICT.md §4). It is the only
// rendering path: it reads exclusively off the Verdict value also used to
// produce the JSON document, so machine and human output can never drift
// apart (R-VER-9).
func Render(v Verdict) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s  %s", strings.ToUpper(string(v.Status)), v.Scenario)
	if v.DurationS > 0 {
		fmt.Fprintf(&b, "  %ds", v.DurationS)
	}
	if v.Commit != "" {
		fmt.Fprintf(&b, "  commit %s", v.Commit)
	}
	b.WriteString("\n\n")

	for _, f := range v.Findings {
		fmt.Fprintf(&b, "  ✗ %s -> %s", f.Broke.Assertion, f.Broke.Observed)
		if f.Broke.At != "" {
			fmt.Fprintf(&b, "   at %s", f.Broke.At)
		}
		if f.Broke.SustainedS > 0 {
			fmt.Fprintf(&b, ", sustained %ds", f.Broke.SustainedS)
		}
		b.WriteString("\n")

		if f.Cause != nil {
			fmt.Fprintf(&b, "    caused by  %s (%s)", f.Cause.Fault, f.Cause.Target)
			fmt.Fprintf(&b, "  [confidence: %s]\n", f.Confidence)
		} else {
			fmt.Fprintf(&b, "    [confidence: %s]\n", f.Confidence)
		}
		b.WriteString("\n")

		for _, hop := range f.Chain {
			fmt.Fprintf(&b, "    %-14s%s\n", hop.At, hop.Observed)
		}
		if len(f.Chain) > 0 {
			b.WriteString("\n")
		}

		if f.Amplification != "" {
			fmt.Fprintf(&b, "    %s\n\n", f.Amplification)
		}

		if len(f.Candidates) > 0 {
			b.WriteString("    look at:")
			for i, c := range f.Candidates {
				prefix := "  "
				if i > 0 {
					prefix = "\n              "
				}
				fmt.Fprintf(&b, "%s%s %s", prefix, c.Library, strings.Join(c.Knobs, ", "))
			}
			b.WriteString("\n\n")
		}
	}

	for _, p := range v.Passed {
		fmt.Fprintf(&b, "  ✓ %s     %s\n", p.Assertion, p.Observed)
	}
	if len(v.Passed) > 0 {
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "  egress: %d mocked, %d blocked, %d real",
		len(v.EgressAudit.Mocked), len(v.EgressAudit.Blocked), len(v.EgressAudit.Real))
	fmt.Fprintf(&b, "          exit %d\n", ExitCode(v))

	return b.String()
}
