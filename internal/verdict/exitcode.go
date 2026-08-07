package verdict

// ExitCode maps a Verdict's status to the process exit code the CLI must
// return (R-VER-7):
//
//	0 pass
//	1 fail         — an assertion broke
//	2 error        — TortureU or an adapter failed
//	3 aborted      — unclassified egress, or reset failed
//	4 inconclusive — ran clean (status=fail), but every finding is `ambiguous`
//
// R-VER-8: 4 is never returned for status=pass, and must never be mistaken
// for success by a caller checking exit==0.
func ExitCode(v Verdict) int {
	switch v.Status {
	case StatusPass:
		return 0
	case StatusError:
		return 2
	case StatusAborted:
		return 3
	case StatusFail:
		if allAmbiguous(v.Findings) {
			return 4
		}
		return 1
	default:
		return 2
	}
}

// allAmbiguous reports whether every finding is ambiguous. A fail status with
// zero findings is not "inconclusive" — findings are only empty on pass
// (VERDICT.md §1) — so this only fires with at least one finding present.
func allAmbiguous(findings []Finding) bool {
	if len(findings) == 0 {
		return false
	}
	for _, f := range findings {
		if f.Confidence != Ambiguous {
			return false
		}
	}
	return true
}
