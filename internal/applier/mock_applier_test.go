package applier

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// spec: R-EXE-15
func TestErrorRateMappings_CycleHasExactlyRoundedErrorCount(t *testing.T) {
	// error_rate is a proportion of responses (R-CFG-23), approximated here
	// as a deterministic cycle over errorRateCycleLen states so the exact
	// fraction that fails is verifiable rather than merely "probably around
	// the right rate" — see the package doc on errorRateMappings.
	mappings := errorRateMappings("f1", ErrorRate{Target: "api.stripe.com", Rate: 0.5, Status: 503})
	if len(mappings) != errorRateCycleLen {
		t.Fatalf("len(mappings) = %d, want %d (one per cycle state)", len(mappings), errorRateCycleLen)
	}
	errorCount := 0
	for _, m := range mappings {
		if m.Response.Status == 503 {
			errorCount++
		} else if m.Response.Status != 200 {
			t.Errorf("mapping response status = %d, want 503 or 200", m.Response.Status)
		}
	}
	wantErrors := errorRateCycleLen / 2 // rate 0.5
	if errorCount != wantErrors {
		t.Errorf("errorCount = %d, want %d for rate 0.5", errorCount, wantErrors)
	}
}

// spec: R-EXE-17
func TestErrorRateMappings_ZeroRateFailsNothing(t *testing.T) {
	mappings := errorRateMappings("f1", ErrorRate{Target: "api.stripe.com", Rate: 0.0, Status: 500})
	for _, m := range mappings {
		if m.Response.Status != 200 {
			t.Fatalf("rate 0.0 produced a %d response, want every state to pass (200)", m.Response.Status)
		}
	}
}

// spec: R-CFG-23
func TestErrorRateMappings_FullRateFailsEverything(t *testing.T) {
	mappings := errorRateMappings("f1", ErrorRate{Target: "api.stripe.com", Rate: 1.0, Status: 500})
	for _, m := range mappings {
		if m.Response.Status != 500 {
			t.Fatalf("rate 1.0 produced a %d response, want every state to fail (500)", m.Response.Status)
		}
	}
}

// spec: R-EXE-15
func TestErrorRateMappings_StatesFormOneCycle(t *testing.T) {
	// Every mapping must be scoped to the SAME scenario (so they share one
	// cycle position) and its newScenarioState must be the next state
	// modulo the cycle length, so applying N requests advances exactly N
	// steps and wraps back to the start.
	mappings := errorRateMappings("f1", ErrorRate{Target: "api.stripe.com", Rate: 0.3, Status: 500})
	scenario := mappings[0].ScenarioName
	for i, m := range mappings {
		if m.ScenarioName != scenario {
			t.Fatalf("mapping %d has scenario %q, want all mappings sharing %q", i, m.ScenarioName, scenario)
		}
		wantNext := mappings[(i+1)%len(mappings)].RequiredScenarioState
		if m.NewScenarioState != wantNext {
			t.Errorf("mapping %d newScenarioState = %q, want %q (next state in cycle)", i, m.NewScenarioState, wantNext)
		}
	}
}

// spec: R-EXE-18
func TestWireMockApplier_UndoDeletesExactlyTheCreatedMappings(t *testing.T) {
	var mu sync.Mutex
	created := map[string]bool{}
	var deleted []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/__admin/mappings":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			id := fmt.Sprintf("id-%d", len(created))
			mu.Lock()
			created[id] = true
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/__admin/mappings/"):
			id := strings.TrimPrefix(r.URL.Path, "/__admin/mappings/")
			mu.Lock()
			deleted = append(deleted, id)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := &WireMockApplier{BaseURL: srv.URL}
	undo, _, err := a.ApplyErrorRate("stripe_errors", ErrorRate{Target: "api.stripe.com", Rate: 0.5, Status: 503})
	if err != nil {
		t.Fatalf("ApplyErrorRate: %v", err)
	}
	if len(created) != errorRateCycleLen {
		t.Fatalf("created %d mappings, want %d", len(created), errorRateCycleLen)
	}
	if err := undo(); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if len(deleted) != len(created) {
		t.Fatalf("deleted %d mappings, want %d (exactly what was created — R-EXE-18: this applier CAN fully reverse a stub, unlike a published queue message)", len(deleted), len(created))
	}
	for _, id := range deleted {
		if !created[id] {
			t.Errorf("undo deleted %q, which this applier never created", id)
		}
	}
}

// spec: R-EXE-19
func TestWireMockApplier_ApplyErrorRateErrorsOnNon2xxFromWireMock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	a := &WireMockApplier{BaseURL: srv.URL}
	if _, _, err := a.ApplyErrorRate("f1", ErrorRate{Target: "api.stripe.com", Rate: 0.5, Status: 503}); err == nil {
		t.Fatal("ApplyErrorRate returned nil error when WireMock rejected the mapping, want an error — a fault that silently fails to apply must not report success (R-EXE-19)")
	}
}

// spec: R-EXE-22
func TestApplyErrorRate_ReportsApproximationForSubResolutionRate(t *testing.T) {
	// 0.17 is not a multiple of the cycle's 5% resolution (1/20): it rounds
	// to 0.17*20=3.4 -> 3 states -> 3/20 = 0.15. R-EXE-22 requires this be
	// reported as an approximation, with both the requested and applied
	// values visible, rather than silently rounded.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "id-1"})
	}))
	defer srv.Close()

	a := &WireMockApplier{BaseURL: srv.URL}
	_, result, err := a.ApplyErrorRate("f1", ErrorRate{Target: "api.stripe.com", Rate: 0.17, Status: 500})
	if err != nil {
		t.Fatalf("ApplyErrorRate: %v", err)
	}
	if !result.Approximated {
		t.Error("Approximated = false for rate 0.17, want true — this rate is finer than the 5% cycle resolution")
	}
	if result.Requested != 0.17 {
		t.Errorf("Requested = %v, want 0.17 (the value actually asked for, not the rounded one)", result.Requested)
	}
	if result.Applied != 0.15 {
		t.Errorf("Applied = %v, want 0.15 (3/20 — what was actually wired into WireMock)", result.Applied)
	}
}

// spec: R-EXE-22
func TestApplyErrorRate_ExactResolutionRateNotReportedApproximated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "id-1"})
	}))
	defer srv.Close()

	a := &WireMockApplier{BaseURL: srv.URL}
	for _, rate := range []float64{0.15, 0.20} {
		_, result, err := a.ApplyErrorRate("f1", ErrorRate{Target: "api.stripe.com", Rate: rate, Status: 500})
		if err != nil {
			t.Fatalf("ApplyErrorRate(%v): %v", rate, err)
		}
		if result.Approximated {
			t.Errorf("Approximated = true for rate %v, want false — it is an exact multiple of the 5%% cycle resolution", rate)
		}
		if result.Applied != rate {
			t.Errorf("Applied = %v, want %v (exact)", result.Applied, rate)
		}
	}
}

// wireMockAvailable skips a test rather than passing it when no Docker
// daemon is reachable — a Docker-dependent guarantee proven only "when
// Docker happens to be there" would be worse than no test.
func wireMockAvailable(t *testing.T) string {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}
	out, err := exec.Command("docker", "run", "-d", "-p", "0:8080", "wiremock/wiremock:3.9.1-alpine").Output()
	if err != nil {
		t.Skipf("docker run wiremock: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })

	portOut, err := exec.Command("docker", "port", id, "8080/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	addr := strings.TrimSpace(strings.Split(string(portOut), "\n")[0])
	baseURL := "http://" + addr

	// Wait for WireMock to be ready.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/__admin/mappings")
		if err == nil {
			resp.Body.Close()
			return baseURL
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("wiremock never became ready")
	return ""
}

// hitAndCountFailures issues n GET requests against target and returns how
// many came back with wantStatus.
func hitAndCountFailures(t *testing.T, baseURL string, n, wantStatus int) int {
	t.Helper()
	failures := 0
	for i := 0; i < n; i++ {
		resp, err := http.Get(baseURL + "/anything")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		if resp.StatusCode == wantStatus {
			failures++
		}
		resp.Body.Close()
	}
	return failures
}

// spec: R-EXE-19
func TestWireMockApplier_AppliesErrorRateAgainstRealWireMockThenUndoRestoresDefault(t *testing.T) {
	baseURL := wireMockAvailable(t)

	a := &WireMockApplier{BaseURL: baseURL}
	rate := 0.5
	wantFailures := int(rate * float64(errorRateCycleLen))

	// Negative control: before applying anything, WireMock's built-in
	// default (no stub matches "/anything") is 404, never our configured
	// failure status — proving the later assertion is measuring something
	// this applier actually did, not WireMock's baseline behaviour.
	if before := hitAndCountFailures(t, baseURL, errorRateCycleLen, 503); before != 0 {
		t.Fatalf("before ApplyErrorRate: %d/%d requests already returned 503, want 0 (bad test target)", before, errorRateCycleLen)
	}

	undo, result, err := a.ApplyErrorRate("stripe_errors", ErrorRate{Target: "api.stripe.com", Rate: rate, Status: 503})
	if err != nil {
		t.Fatalf("ApplyErrorRate: %v", err)
	}
	if result.Approximated {
		t.Errorf("rate %v is an exact multiple of the cycle resolution, want Approximated = false", rate)
	}

	got := hitAndCountFailures(t, baseURL, errorRateCycleLen, 503)
	if got != wantFailures {
		t.Errorf("failures = %d/%d, want exactly %d for rate %v (R-CFG-23: a proportion, not approximate)", got, errorRateCycleLen, wantFailures, rate)
	}

	if err := undo(); err != nil {
		t.Fatalf("undo: %v", err)
	}

	// R-EXE-18: undo for the mock provider IS a real reversal (unlike the
	// broker producer) — after it, no configured failure remains.
	after := hitAndCountFailures(t, baseURL, errorRateCycleLen, 503)
	if after != 0 {
		t.Errorf("after undo: %d/%d requests still returned 503, want 0 — undo must remove the stub, not merely stop future injection", after, errorRateCycleLen)
	}
}
