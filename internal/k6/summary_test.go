package k6

import "testing"

// spec: R-VER-10
// k6 results MUST be ingested from its machine-readable JSON
// (handleSummary() output, or --out json jsonlines). The human CLI summary
// MUST NOT be parsed, since it carries no stability guarantee.
func TestIngestSummary_ParsesJSONNeverHumanText(t *testing.T) {
	raw := []byte(`{
		"metrics": {
			"http_req_duration": {"values": {"p(95)": 210.4, "p(99)": 480.1}},
			"http_req_failed": {"values": {"rate": 0.002}}
		}
	}`)

	metrics, err := IngestSummary(raw)
	if err != nil {
		t.Fatalf("IngestSummary: %v", err)
	}
	if _, ok := metrics["http_req_duration"]; !ok {
		t.Errorf("metrics missing http_req_duration: %+v", metrics)
	}
	if _, ok := metrics["http_req_failed"]; !ok {
		t.Errorf("metrics missing http_req_failed: %+v", metrics)
	}

	// A k6 human CLI summary is not JSON at all; feeding it in MUST error
	// rather than being silently scraped.
	humanSummary := []byte(`
     ✓ status is 200

     checks.........................: 100.00% ✓ 120      ✗ 0
     http_req_duration..............: avg=12ms p(95)=210ms
`)
	if _, err := IngestSummary(humanSummary); err == nil {
		t.Errorf("IngestSummary(humanSummary): want error, got nil")
	}
}

// spec: R-VER-10
// A real k6 0.54.0 --summary-export document (confirmed by running the
// pinned grafana/k6:0.54.0 image directly) encodes each per-threshold
// result as a raw JSON boolean -- {"thresholds": {"p(95)<2000": true}} --
// not the {"ok": true} shape internal/run's evaluateThresholds expects.
// Passed through unmodified, a passing raw `true` fails the type assertion
// `.(map[string]any)`, so `ok` silently reads as false for a threshold k6
// itself reported as passing -- a real run's first two thresholds ("p(95)
// <2000 -> 0.583" and "rate<0.5 -> 0", both genuinely under their limit)
// both came back ok:false and the run exited 4 (inconclusive) instead of
// 0. IngestSummary is the sole boundary that parses k6's raw JSON shape
// (R-VER-10), so it must normalize every threshold result to {"ok": bool}
// before handing metrics to any downstream caller, rather than passing
// k6's boolean through as an opaque value only some k6 versions produce
// this way.
func TestIngestSummary_NormalizesBooleanThresholdResults(t *testing.T) {
	// Units pinned explicitly: k6 reports http_req_duration percentiles in
	// milliseconds. 583 here means 583ms, well under the 2000ms threshold --
	// not 0.583s and not 583ns. The bug this test guards against was masked,
	// not caused, by a unit mismatch, but pinning the unit keeps a future
	// regression that reintroduces one from hiding again.
	raw := []byte(`{
		"metrics": {
			"http_req_duration": {
				"p(95)": 583,
				"thresholds": {"p(95)<2000": true}
			},
			"http_req_failed": {
				"value": 0,
				"thresholds": {"rate<0.5": true}
			}
		}
	}`)

	metrics, err := IngestSummary(raw)
	if err != nil {
		t.Fatalf("IngestSummary: %v", err)
	}

	durMetric, ok := metrics["http_req_duration"].(map[string]any)
	if !ok {
		t.Fatalf("http_req_duration is not a map: %+v", metrics["http_req_duration"])
	}
	if p95 := durMetric["p(95)"]; p95 != 583.0 {
		t.Errorf("p(95) = %v, want 583 (milliseconds, k6's native unit)", p95)
	}
	durThresholds, ok := durMetric["thresholds"].(map[string]any)
	if !ok {
		t.Fatalf("http_req_duration.thresholds is not a map: %+v", durMetric["thresholds"])
	}
	durResult, ok := durThresholds["p(95)<2000"].(map[string]any)
	if !ok {
		t.Fatalf("p(95)<2000 threshold result was not normalized to a map: %#v", durThresholds["p(95)<2000"])
	}
	if okVal, _ := durResult["ok"].(bool); !okVal {
		t.Errorf("p(95)<2000 threshold: want ok=true (583 < 2000), got %+v", durResult)
	}

	failedMetric, ok := metrics["http_req_failed"].(map[string]any)
	if !ok {
		t.Fatalf("http_req_failed is not a map: %+v", metrics["http_req_failed"])
	}
	failedThresholds, ok := failedMetric["thresholds"].(map[string]any)
	if !ok {
		t.Fatalf("http_req_failed.thresholds is not a map: %+v", failedMetric["thresholds"])
	}
	failedResult, ok := failedThresholds["rate<0.5"].(map[string]any)
	if !ok {
		t.Fatalf("rate<0.5 threshold result was not normalized to a map: %#v", failedThresholds["rate<0.5"])
	}
	if okVal, _ := failedResult["ok"].(bool); !okVal {
		t.Errorf("rate<0.5 threshold: want ok=true (0 < 0.5), got %+v", failedResult)
	}
}
