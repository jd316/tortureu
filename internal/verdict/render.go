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

	// R-VER-8: exit 4 (inconclusive) means the run genuinely completed but
	// every finding is ambiguous/unevaluated — the human header must agree
	// with that exit code rather than reading FAIL when nothing was actually
	// shown to have failed. This changes only the displayed word, not the
	// underlying Status value (still "fail" in the JSON document, per
	// R-VER-2's closed status enum) — one document, one renderer, no second
	// source of truth (R-VER-9).
	label := strings.ToUpper(string(v.Status))
	if ExitCode(v) == 4 {
		label = "INCONCLUSIVE"
	}
	fmt.Fprintf(&b, "%s  %s", label, v.Scenario)
	if v.DurationS > 0 {
		fmt.Fprintf(&b, "  %ds", v.DurationS)
	}
	if v.Commit != "" {
		fmt.Fprintf(&b, "  commit %s", v.Commit)
	}
	b.WriteString("\n")

	// R-DC2-2: an abort MUST name every unclassified host — this is the
	// reason a user gets exit 3, and it must be impossible to miss.
	if len(v.EgressAudit.Unclassified) > 0 {
		fmt.Fprintf(&b, "\n  unclassified egress, run refused: %s\n",
			strings.Join(v.EgressAudit.Unclassified, ", "))
	}

	// A non-clean reset is the other legitimate abort cause (R-VER-7: exit 3
	// is "unclassified egress, or reset failed") and was otherwise invisible
	// to a human even though it's already in the JSON document.
	if v.Reset != "" && v.Reset != "clean" {
		fmt.Fprintf(&b, "\n  reset: %s\n", v.Reset)
	}

	// R-VER-2: status=error means TortureU itself broke, distinct from
	// status=fail (the SUT broke an assertion). That distinction only helps a
	// user if the tool says what broke — an error with no reason is
	// indistinguishable from a shrug, so surface it prominently.
	if v.Status == StatusError && v.Error != "" {
		fmt.Fprintf(&b, "\n  error: %s\n", v.Error)
	}

	b.WriteString("\n")

	for _, f := range v.Findings {
		// R-VER-8: an unevaluated assertion gets its own marker and no
		// comparison arrow — never a measured value next to a fail symbol,
		// since no measurement was taken to compare.
		if f.Unevaluated {
			fmt.Fprintf(&b, "  ? %s — not evaluated", f.Broke.Assertion)
			if f.Reason != "" {
				fmt.Fprintf(&b, ": %s", f.Reason)
			}
			fmt.Fprintf(&b, "\n    [confidence: %s]\n\n", f.Confidence)
			continue
		}

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

	fmt.Fprintf(&b, "  egress: %d mocked, %d blocked, %d real, %d unclassified",
		len(v.EgressAudit.Mocked), len(v.EgressAudit.Blocked), len(v.EgressAudit.Real),
		len(v.EgressAudit.Unclassified))
	fmt.Fprintf(&b, "          exit %d\n", ExitCode(v))

	return b.String()
}
