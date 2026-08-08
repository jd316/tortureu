package smoke

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// spec: R-CLI-6
func TestRunHitsHealthyServerAtConstantRate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	opts := Options{Rate: 20, Duration: 300 * time.Millisecond, Timeout: time.Second}
	res := Run(srv.Client(), srv.URL, opts)

	// A constant-rate driver at 20/s for 300ms should send a handful of
	// requests, not zero and not thousands — a generous band, since exact
	// tick timing is not this test's concern (Options.Rate/Duration are).
	if res.Sent < 3 || res.Sent > 15 {
		t.Errorf("Sent = %d, want roughly 6 (20/s * 0.3s)", res.Sent)
	}
	if res.OK != res.Sent {
		t.Errorf("OK = %d, want %d (server always 200)", res.OK, res.Sent)
	}
	if res.Failed != 0 {
		t.Errorf("Failed = %d, want 0", res.Failed)
	}
	if res.P50 <= 0 || res.P95 <= 0 || res.P99 <= 0 {
		t.Errorf("expected positive latency percentiles, got p50=%v p95=%v p99=%v", res.P50, res.P95, res.P99)
	}
}

// spec: R-CLI-6
func TestRunCountsNonSuccessStatusAsFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	opts := Options{Rate: 20, Duration: 200 * time.Millisecond, Timeout: time.Second}
	res := Run(srv.Client(), srv.URL, opts)

	if res.Sent == 0 {
		t.Fatal("Sent = 0, want at least one request")
	}
	if res.OK != 0 {
		t.Errorf("OK = %d, want 0 (server always 500)", res.OK)
	}
	if res.Failed != res.Sent {
		t.Errorf("Failed = %d, want %d", res.Failed, res.Sent)
	}
}

// spec: R-CLI-6
func TestRunCountsConnectionErrorsAsFailed(t *testing.T) {
	// A closed server: every dial fails, but Run must still finish inside
	// its duration budget rather than hang waiting on a connection that
	// will never succeed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	opts := Options{Rate: 20, Duration: 200 * time.Millisecond, Timeout: 200 * time.Millisecond}
	start := time.Now()
	res := Run(http.DefaultClient, srv.URL, opts)
	elapsed := time.Since(start)

	if res.Sent == 0 {
		t.Fatal("Sent = 0, want at least one attempted request")
	}
	if res.OK != 0 {
		t.Errorf("OK = %d, want 0 (server is down)", res.OK)
	}
	if res.Failed != res.Sent {
		t.Errorf("Failed = %d, want %d", res.Failed, res.Sent)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Run took %v, want it bounded well under 2s", elapsed)
	}
}

func TestPercentileOfEmptyIsZero(t *testing.T) {
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("percentile(nil, 0.5) = %v, want 0", got)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	durs := []time.Duration{
		10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond,
		40 * time.Millisecond, 50 * time.Millisecond,
	}
	if got := percentile(durs, 0.5); got != 30*time.Millisecond {
		t.Errorf("p50 = %v, want 30ms", got)
	}
	if got := percentile(durs, 0.99); got != 50*time.Millisecond {
		t.Errorf("p99 = %v, want 50ms", got)
	}
}

// spec: R-CLI-6
func TestExitCodeMapsToRVER7Meanings(t *testing.T) {
	cases := []struct {
		name           string
		res            Result
		minSuccessRate float64
		want           int
	}{
		{"nothing sent is an error, not a pass", Result{}, 1.0, 2},
		{"full success at threshold passes", Result{Sent: 10, OK: 10}, 1.0, 0},
		{"below threshold fails", Result{Sent: 10, OK: 8}, 1.0, 1},
		{"below threshold but within a relaxed threshold passes", Result{Sent: 10, OK: 8}, 0.5, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitCode(tc.res, tc.minSuccessRate); got != tc.want {
				t.Errorf("ExitCode(%+v, %v) = %d, want %d", tc.res, tc.minSuccessRate, got, tc.want)
			}
		})
	}
}

// spec: R-CLI-6
func TestRenderReportsCountsAndLatencies(t *testing.T) {
	res := Result{Sent: 10, OK: 9, Failed: 1, P50: 12 * time.Millisecond, P95: 40 * time.Millisecond, P99: 55 * time.Millisecond}
	out := Render("http://example.test", res)
	for _, want := range []string{"http://example.test", "sent: 10", "ok: 9", "failed: 1", "90.0%", "p50", "p95", "p99"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render output missing %q:\n%s", want, out)
		}
	}
}
