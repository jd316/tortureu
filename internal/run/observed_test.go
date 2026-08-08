package run

import "testing"

// spec: R-VER-15
//
// Real k6 --summary-export carries no "contains" field, so the unit rule
// that keyed off contains:"time" never fired against the actual tool and a
// duration rendered as a bare 13-decimal float. The unit comes from the
// metric name instead: k6's *_duration trends are milliseconds.
func TestFormatObserved_DurationCarriesMilliseconds(t *testing.T) {
	got := formatObserved("http_req_duration", 3003.2139021999997)
	if got != "3003.21ms" {
		t.Fatalf("formatObserved = %q, want %q", got, "3003.21ms")
	}
}

// spec: R-VER-15
//
// Significant figures, not fixed decimals: a rate and a duration share one
// document, and two decimal places would round 0.003 away to 0.00.
func TestFormatObserved_RateKeepsItsMagnitude(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0.003, "0.003"},
		{0.6659242761692651, "0.665924"},
		{0, "0"},
	} {
		if got := formatObserved("http_req_failed", tc.in); got != tc.want {
			t.Errorf("formatObserved(http_req_failed, %v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// spec: R-VER-15
//
// An unknown metric gets no unit. Guessing one would state a wrong
// measurement in a document read as evidence.
func TestFormatObserved_UnknownMetricGetsNoUnit(t *testing.T) {
	got := formatObserved("orders_in_flight", 47.5)
	if got != "47.5" {
		t.Fatalf("formatObserved = %q, want a bare value %q", got, "47.5")
	}
}
