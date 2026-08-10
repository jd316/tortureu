package run

import (
	"encoding/json"
	"os"
	"testing"
)

// spec: R-VER-19
//
// Two summaries produced by a real grafana/k6:latest (v2.1.0) container and
// committed verbatim under testdata/ — one where the threshold held, one where
// k6 itself printed "thresholds have been crossed". The parser must agree with
// what k6 actually did, not with what the bool literally says, and the
// evidence has to travel with the test rather than living on one machine.
func TestThresholdHeld_AgainstRealK6V2Summaries(t *testing.T) {
	for _, tc := range []struct {
		path string
		held bool
	}{
		{"testdata/k6v2_summary_threshold_held.json", true},   // p(95)<5000 — k6 reported no crossing
		{"testdata/k6v2_summary_threshold_broke.json", false}, // p(95)<1 — k6 reported "thresholds crossed"
	} {
		raw, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("committed k6 v2 summary missing: %v", err)
		}
		var doc struct {
			Metrics map[string]struct {
				Thresholds map[string]any `json:"thresholds"`
			} `json:"metrics"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		for expr, v := range doc.Metrics["http_req_duration"].Thresholds {
			got, err := thresholdHeld(v)
			if err != nil {
				t.Fatalf("%s %q: %v", tc.path, expr, err)
			}
			if got != tc.held {
				t.Errorf("%s %q: held = %v, want %v (raw %v)", tc.path, expr, got, tc.held, v)
			}
		}
	}
}
