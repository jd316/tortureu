package k6

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// summaryDoc models enough of k6's handleSummary() JSON export to extract
// metrics (R-VER-10). k6's own summary object carries far more (root_group,
// options, state, ...); this package only needs the metrics map, since that
// is what becomes verdict.Verdict.Metrics.
type summaryDoc struct {
	Metrics map[string]any `json:"metrics"`
}

// IngestSummary extracts the metrics map from k6's machine-readable JSON
// summary output (handleSummary(), or one jsonlines record from --out
// json). It never parses k6's human CLI summary text, which carries no
// stability guarantee (R-VER-10). The returned map is the value to assign
// to verdict.Verdict.Metrics.
//
// Every threshold's ok verdict is recomputed here from the metric's own
// measured value and the threshold expression Compile generated, rather
// than trusted from whatever boolean k6 itself wrote next to it. This is
// not optional hygiene: an E1 eval, and this function's own tests below,
// found that k6 0.54.0's `--summary-export` writer reports *every*
// threshold as failed on the `ramping-arrival-rate` executor -- the one
// executor R-CFG-6 permits -- regardless of the measured value, even when
// k6's own CLI output for the identical run shows a passing checkmark, and
// even when a script's handleSummary() explicitly overwrites that
// threshold's `ok` to true before returning: --summary-export's writer
// recomputes its own answer independently and ignores what handleSummary()
// returned for the same path. Trusting either supplied boolean means every
// real run reports every assertion as broken. Recomputing from the
// measured value sidesteps k6's writer entirely, in both the executor and
// the output-path combination that broke.
func IngestSummary(raw []byte) (map[string]any, error) {
	var doc summaryDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("k6: summary is not valid JSON (R-VER-10 forbids parsing the human CLI summary): %w", err)
	}
	if doc.Metrics == nil {
		return nil, fmt.Errorf("k6: summary JSON has no \"metrics\" field")
	}
	recomputeThresholds(doc.Metrics)
	return doc.Metrics, nil
}

// thresholdComparisonOps are tried longest-first so "<=" is not mistaken
// for "<" at the same starting index.
var thresholdComparisonOps = []string{"<=", ">=", "==", "!=", "<", ">"}

// splitThresholdExpr splits a k6 threshold expression ("p(95)<2000") into
// the statistic name it names ("p(95)") and the operator+value to compare
// against ("<2000" -> op "<", limit 2000). ok is false for a shape this
// cannot parse, e.g. no known operator or a non-numeric right-hand side.
func splitThresholdExpr(expr string) (statKey, op string, limit float64, ok bool) {
	idx, foundOp := -1, ""
	for _, o := range thresholdComparisonOps {
		if i := strings.Index(expr, o); i != -1 && (idx == -1 || i < idx) {
			idx, foundOp = i, o
		}
	}
	if idx == -1 {
		return "", "", 0, false
	}
	statKey = strings.TrimSpace(expr[:idx])
	limitStr := strings.TrimSpace(expr[idx+len(foundOp):])
	limit, err := strconv.ParseFloat(limitStr, 64)
	if err != nil {
		return "", "", 0, false
	}
	return statKey, foundOp, limit, true
}

// lookupMetricStat reads a named statistic (e.g. "p(95)", "rate") off a
// metric object. Real k6 puts every statistic directly on the metric
// object in `--summary-export` output, or nested under a "values"
// sub-object in handleSummary()'s data -- both shapes are checked. A
// Rate-typed metric's threshold names its stat "rate" (e.g. "rate<0.01")
// but the metric object itself carries that number as "value" with no
// "rate" key at all (confirmed against real k6 output), so "rate" falls
// back to "value".
func lookupMetricStat(m map[string]any, statKey string) (float64, bool) {
	raw, ok := m[statKey]
	if !ok {
		if values, ok2 := m["values"].(map[string]any); ok2 {
			raw, ok = values[statKey]
		}
	}
	if !ok && statKey == "rate" {
		return lookupMetricStat(m, "value")
	}
	v, ok := raw.(float64)
	return v, ok
}

// compare applies a threshold's comparison operator. ok is false for an
// operator splitThresholdExpr should never actually produce (defensive
// only; thresholdComparisonOps and this switch are kept in lockstep).
func compare(value float64, op string, limit float64) (result, ok bool) {
	switch op {
	case "<":
		return value < limit, true
	case "<=":
		return value <= limit, true
	case ">":
		return value > limit, true
	case ">=":
		return value >= limit, true
	case "==":
		return value == limit, true
	case "!=":
		return value != limit, true
	default:
		return false, false
	}
}

// recomputeThresholds rewrites every metric's per-threshold result to
// {"ok": bool}, computed from that metric's own measured statistic and the
// threshold expression -- never from whatever value k6 itself supplied
// there (see IngestSummary's doc comment for why). A threshold whose
// expression cannot be parsed, or whose named statistic cannot be found on
// the metric, is left as {"ok": false}: R-VER-8/D-9's honesty rule applies
// here too -- an assertion that cannot be verified must never be reported
// as having passed, so the failure-safe default is "not proven true", not
// "presumed true".
func recomputeThresholds(metrics map[string]any) {
	for _, v := range metrics {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		thresholds, ok := m["thresholds"].(map[string]any)
		if !ok {
			continue
		}
		for expr := range thresholds {
			thresholds[expr] = map[string]any{"ok": evaluateThreshold(m, expr)}
		}
	}
}

// evaluateThreshold reports whether metric m's own measured statistic
// satisfies threshold expression expr. False whenever expr cannot be
// parsed or the statistic cannot be found -- never defaulted to true.
func evaluateThreshold(m map[string]any, expr string) bool {
	statKey, op, limit, ok := splitThresholdExpr(expr)
	if !ok {
		return false
	}
	value, ok := lookupMetricStat(m, statKey)
	if !ok {
		return false
	}
	result, ok := compare(value, op, limit)
	return ok && result
}
