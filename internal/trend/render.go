package trend

import (
	"fmt"
	"sort"
	"strings"
)

// abbrev shortens a full 40-character anchor for display only. R-VER-12
// stores the full hash precisely because an abbreviated one can collide; this
// is the same allowance VERDICT.md §4 makes for the human rendering.
func abbrev(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// Render is the human view of a trend (R-CLI-14). It answers both questions a
// cross-commit trend exists for — did a number move, and did a finding appear
// that was not there before — and it states plainly what it left out, because
// a trend that silently omits runs is worse than no trend.
func Render(rep Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "trend: %s\n", rep.Path)
	if len(rep.Rows) == 0 && rep.Unanchored == 0 {
		b.WriteString("\n  no runs recorded yet — `tortureu run -json | tortureu trend record -`\n")
		renderNotes(&b, rep)
		return b.String()
	}
	fmt.Fprintf(&b, "%d run(s) in the series", len(rep.Rows))
	if rep.Filter.Scenario != "" {
		fmt.Fprintf(&b, ", scenario %q", rep.Filter.Scenario)
	}
	if rep.Filter.Metric != "" {
		fmt.Fprintf(&b, ", metrics matching %q", rep.Filter.Metric)
	}
	b.WriteString("\n")

	// Group by scenario, in first-appearance order: two scenarios are two
	// measurements and share no baseline, so they are two series.
	var order []string
	byScenario := map[string][]Row{}
	for _, r := range rep.Rows {
		if _, seen := byScenario[r.Scenario]; !seen {
			order = append(order, r.Scenario)
		}
		byScenario[r.Scenario] = append(byScenario[r.Scenario], r)
	}

	// One key column for the whole report, wide enough for the longest key
	// actually shown. k6 emits sub-metric keys like
	// http_req_duration{expected_response:true}.p(95), and a fixed width
	// makes those overflow into the number column — which is the one column
	// a trend is read down.
	keyWidth := 30
	for _, k := range rep.Metrics {
		if len(k) > keyWidth {
			keyWidth = len(k)
		}
	}

	for _, sc := range order {
		name := sc
		if name == "" {
			name = "(unnamed scenario)"
		}
		fmt.Fprintf(&b, "\n%s\n", name)
		for _, r := range byScenario[sc] {
			fmt.Fprintf(&b, "  %-7s  %-20s  %-7s exit %d\n",
				abbrev(r.Commit), r.StartedAt, r.Status, r.ExitCode)
			if !r.Comparable {
				fmt.Fprintf(&b, "      (status %s carries no measurement of the system under test — "+
					"no comparison, and the next run compares against the last measured one)\n", r.Status)
				continue
			}
			keys := make([]string, 0, len(r.Metrics))
			for k := range r.Metrics {
				if rep.Filter.Metric == "" || strings.Contains(k, rep.Filter.Metric) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				d, ok := r.Deltas[k]
				switch {
				case !ok:
					fmt.Fprintf(&b, "      %-*s %14s\n", keyWidth, k, num(r.Metrics[k]))
				default:
					fmt.Fprintf(&b, "      %-*s %14s  %12s\n", keyWidth, k, num(r.Metrics[k]), signed(d))
				}
			}
			for _, f := range r.NewFindings {
				fmt.Fprintf(&b, "      NEW finding      %s\n", f)
			}
			for _, f := range r.GoneFindings {
				fmt.Fprintf(&b, "      GONE finding     %s\n", f)
			}
		}
	}
	renderNotes(&b, rep)
	return b.String()
}

// renderNotes states what the series does not contain and why.
func renderNotes(b *strings.Builder, rep Report) {
	if rep.Unanchored > 0 {
		fmt.Fprintf(b, "\n%d run(s) excluded: no commit anchor.\n"+
			"  R-VER-12 leaves `commit` empty when the run was not made in a git checkout, and a row\n"+
			"  with no anchor cannot join a trend — every such run would collapse onto one point. The\n"+
			"  records are kept in the store; they are just never compared.\n", rep.Unanchored)
	}
	if len(rep.Skipped) > 0 {
		fmt.Fprintf(b, "\n%d line(s) of the store could not be read (kept on disk, not counted):\n", len(rep.Skipped))
		for _, s := range rep.Skipped {
			fmt.Fprintf(b, "  line %d: %s\n", s.Line, s.Reason)
		}
	}
}

// num formats a metric value without exponent noise and without pretending to
// a precision the measurement does not have.
func num(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%.3f", f)
}

// signed renders a delta with its sign always shown, so "no change" (0) reads
// differently from "not compared" (a blank column).
func signed(f float64) string {
	s := num(f)
	if f >= 0 {
		return "+" + s
	}
	return s
}
