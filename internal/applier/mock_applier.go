package applier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
)

// errorRateCycleLen is the number of WireMock scenario states
// errorRateMappings cycles through to approximate a Rate. WireMock has no
// native "fail this fraction of requests" primitive (RESEARCH.md's WireMock
// entry: stub + fault + delay + proxy-record — no probability knob among
// them), so this package approximates a proportion the same way SPEC.md
// approaches percentages elsewhere: deterministically. 20 states gives 5%
// granularity, matches R-CFG-23's 0.0..1.0 range exactly at every multiple
// of 0.05, and keeps the mapping count (one HTTP call each way per state)
// small. SPEC.md does not specify this mechanism or its granularity —
// flagged as a gap in the Task 10 report per R-PROC-2/4.
const errorRateCycleLen = 20

// wireMockRequestPattern matches every request WireMock receives for the
// mocked host this applier's BaseURL represents — one WireMock instance per
// mocked host is the natural compose-service shape (RESEARCH.md's WireMock
// entry), so no further host/path discrimination is needed here.
type wireMockRequestPattern struct {
	URLPathPattern string `json:"urlPathPattern"`
	Method         string `json:"method"`
}

type wireMockResponse struct {
	Status int `json:"status"`
}

// wireMockMapping is one WireMock stub mapping, gated to one state of the
// errorRateMappings cycle.
type wireMockMapping struct {
	ScenarioName          string                 `json:"scenarioName"`
	RequiredScenarioState string                 `json:"requiredScenarioState"`
	NewScenarioState      string                 `json:"newScenarioState"`
	Priority              int                    `json:"priority"`
	Request               wireMockRequestPattern `json:"request"`
	Response              wireMockResponse       `json:"response"`
}

func errorRateScenarioName(faultName string) string {
	return "tortureu_error_rate_" + faultName
}

// errorRateStateName names cycle state i. State 0 MUST be WireMock's own
// "Started" constant — every scenario begins there, and it is not a name
// this package controls — so the first mapping in the cycle is reachable
// without a prior transition. Every other state is our own name, scoped by
// faultName so two faults never share a scenario by accident.
func errorRateStateName(faultName string, i int) string {
	if i == 0 {
		return "Started"
	}
	return fmt.Sprintf("%s_s%d", faultName, i)
}

// errorRateApproximationEpsilon bounds float64 rounding noise (e.g.
// 0.15*20 landing on 2.9999999999999996) so a rate that is exact for the
// cycle's resolution is never misreported as approximated.
const errorRateApproximationEpsilon = 1e-9

// approximateErrorRate rounds rate to the nearest multiple of the cycle's
// resolution (1/errorRateCycleLen) and reports whether that rounding
// actually changed the value (R-EXE-22: "a rate finer than the resolution
// MUST be reported as approximated rather than silently rounded"). It is
// the single source of truth for the rounding decision — errorRateMappings
// and ApplyErrorRate both call it, so the mappings actually built and the
// Applied value reported can never disagree.
func approximateErrorRate(rate float64) (errorCount int, applied float64, approximated bool) {
	errorCount = int(rate*float64(errorRateCycleLen) + 0.5) // round half up
	applied = float64(errorCount) / float64(errorRateCycleLen)
	approximated = math.Abs(applied-rate) > errorRateApproximationEpsilon
	return errorCount, applied, approximated
}

// errorRateMappings builds the errorRateCycleLen stub mappings that
// implement r as a deterministic cycle: round(r.Rate * errorRateCycleLen)
// of the states respond r.Status, the rest respond 200, and each state's
// newScenarioState is the next state modulo the cycle length so N requests
// advance N steps and the cycle repeats indefinitely. This is pure request
// shaping — no network access — so it is tested without a WireMock daemon.
func errorRateMappings(faultName string, r ErrorRate) []wireMockMapping {
	scenario := errorRateScenarioName(faultName)
	errorCount, _, _ := approximateErrorRate(r.Rate)

	mappings := make([]wireMockMapping, errorRateCycleLen)
	for i := 0; i < errorRateCycleLen; i++ {
		status := 200
		if i < errorCount {
			status = r.Status
		}
		mappings[i] = wireMockMapping{
			ScenarioName:          scenario,
			RequiredScenarioState: errorRateStateName(faultName, i),
			NewScenarioState:      errorRateStateName(faultName, (i+1)%errorRateCycleLen),
			Priority:              1, // ahead of the mock's own base stub (WireMock default priority 5)
			Request: wireMockRequestPattern{
				URLPathPattern: ".*",
				Method:         "ANY",
			},
			Response: wireMockResponse{Status: status},
		}
	}
	return mappings
}

// WireMockApplier drives one WireMock instance's admin HTTP API — the real
// implementation of the mock-provider side of R-EXE-15's error_rate row.
// BaseURL is that WireMock instance's own address (it serves both stub
// matching and the /__admin API on the same port), e.g.
// "http://wiremock-stripe:8080".
type WireMockApplier struct {
	BaseURL string
	Client  *http.Client
}

func (a *WireMockApplier) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return http.DefaultClient
}

func (a *WireMockApplier) do(method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.BaseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, err
}

// ErrorRateApplied reports what an ApplyErrorRate call actually configured,
// as distinct from what was requested (R-EXE-22). Requested is r.Rate as
// passed in; Applied is the nearest multiple of the cycle's resolution
// (1/errorRateCycleLen) that was actually wired into WireMock; Approximated
// is true when those two differ — i.e. the requested rate was finer than
// this mechanism's resolution and got rounded. A caller that surfaces
// faults in a verdict MUST report Approximated cases so a user tuning a
// threshold at, say, 17% can see they actually got 15%, not silently draw
// a conclusion from a rate that was never applied.
type ErrorRateApplied struct {
	Requested    float64
	Applied      float64
	Approximated bool
}

// ApplyErrorRate registers r as a cycle of WireMock stub mappings (see
// errorRateMappings) and returns an undo that deletes exactly the mappings
// this call created, plus an ErrorRateApplied reporting whether r.Rate had
// to be rounded to this mechanism's resolution (R-EXE-22). Because a stub
// mapping is configuration, not a durable log entry, this undo is a
// genuine reversal (R-EXE-18): after it runs, the mock host's behaviour is
// exactly what it was before ApplyErrorRate — its base capture/spec stub,
// never touched.
func (a *WireMockApplier) ApplyErrorRate(faultName string, r ErrorRate) (func() error, ErrorRateApplied, error) {
	_, applied, approximated := approximateErrorRate(r.Rate)
	result := ErrorRateApplied{Requested: r.Rate, Applied: applied, Approximated: approximated}

	mappings := errorRateMappings(faultName, r)
	ids := make([]string, 0, len(mappings))

	rollback := func() {
		for _, id := range ids {
			a.do(http.MethodDelete, "/__admin/mappings/"+id, nil)
		}
	}

	for _, m := range mappings {
		body, status, err := a.do(http.MethodPost, "/__admin/mappings", m)
		if err != nil {
			rollback()
			return nil, result, fmt.Errorf("applier: wiremock: fault %q: create mapping: %w", faultName, err)
		}
		if status != http.StatusCreated && status != http.StatusOK {
			rollback()
			return nil, result, fmt.Errorf("applier: wiremock: fault %q: create mapping: status %d: %s", faultName, status, body)
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &created); err != nil || created.ID == "" {
			rollback()
			return nil, result, fmt.Errorf("applier: wiremock: fault %q: create mapping: response had no id: %s", faultName, body)
		}
		ids = append(ids, created.ID)
	}

	undo := func() error {
		var firstErr error
		for _, id := range ids {
			_, status, err := a.do(http.MethodDelete, "/__admin/mappings/"+id, nil)
			if err != nil && firstErr == nil {
				firstErr = err
			} else if status != http.StatusOK && status != http.StatusNoContent && firstErr == nil {
				firstErr = fmt.Errorf("applier: wiremock: fault %q: delete mapping %s: status %d", faultName, id, status)
			}
		}
		return firstErr
	}
	return undo, result, nil
}
