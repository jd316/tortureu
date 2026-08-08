package k6

import (
	"encoding/json"
	"fmt"
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
func IngestSummary(raw []byte) (map[string]any, error) {
	var doc summaryDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("k6: summary is not valid JSON (R-VER-10 forbids parsing the human CLI summary): %w", err)
	}
	if doc.Metrics == nil {
		return nil, fmt.Errorf("k6: summary JSON has no \"metrics\" field")
	}
	normalizeThresholds(doc.Metrics)
	return doc.Metrics, nil
}

// normalizeThresholds rewrites every metric's per-threshold result to the
// stable {"ok": bool} shape, in place.
//
// A real k6 0.54.0 `--summary-export` document (confirmed by running the
// pinned grafana/k6:0.54.0 image directly against a live target) encodes
// each threshold result as a bare JSON boolean --
// {"thresholds": {"p(95)<2000": true}} -- not {"p(95)<2000": {"ok": true}}.
// Both shapes have shipped from k6 across versions/output modes. This
// package is the sole point that parses k6's raw JSON (R-VER-10), so it is
// the only place that can absorb that difference; every downstream reader
// (internal/run's evaluateThresholds) gets one shape regardless of which
// one k6 actually emitted. Left unhandled, a bare `true` fails
// `.(map[string]any)`, so a genuinely-passing threshold silently reads as
// unevaluated/failed downstream -- the bug a real run surfaced.
func normalizeThresholds(metrics map[string]any) {
	for _, v := range metrics {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		thresholds, ok := m["thresholds"].(map[string]any)
		if !ok {
			continue
		}
		for expr, result := range thresholds {
			switch r := result.(type) {
			case bool:
				thresholds[expr] = map[string]any{"ok": r}
			case map[string]any:
				// Already in the {"ok": bool} shape; leave as-is.
			}
		}
	}
}
