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
