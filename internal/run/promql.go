// HTTPPromQuerier is the real implementation of PromQuerier (R-CFG-17): an
// instant query against a Prometheus-compatible HTTP API. SPEC.md states
// that a promql: entry must be accepted for signals k6 cannot observe, but
// does not specify pass/fail semantics for an arbitrary PromQL expression —
// escalated in the Task 7 report. This implementation adopts Prometheus's
// own alerting-rule convention: a comparison expression without the `bool`
// modifier (e.g. `sum(rate(x[30s])) < 100`, exactly the shape
// torture.example.yaml uses) is a *filter*, and returns a non-empty vector
// exactly when the condition holds, empty otherwise — the same convention
// `ALERT` rules use to decide whether to fire. holds is therefore "the
// query returned at least one result".
package run

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// HTTPPromQuerier queries a Prometheus (or Prometheus-API-compatible)
// server's instant query endpoint.
type HTTPPromQuerier struct {
	// BaseURL is the Prometheus server, e.g. "http://localhost:9090".
	BaseURL string
	// Client defaults to http.DefaultClient.
	Client *http.Client
}

func (q HTTPPromQuerier) client() *http.Client {
	if q.Client != nil {
		return q.Client
	}
	// fallbackTransport (inreach.go): a direct call first, falling back to
	// reaching the target's own container network namespace only if that
	// fails — the fix for an E1 finding that a DC-2-isolated Prometheus is
	// unreachable as a plain host-process HTTP call, the same shape
	// K6Runner already solves for the SUT itself, one layer over.
	return &http.Client{Transport: fallbackTransport{}}
}

type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string            `json:"resultType"`
		Result     []json.RawMessage `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

// promVectorResult is one element of a Prometheus vector-typed result:
// {"metric": {...}, "value": [<unix ts>, "<value string>"]}.
type promVectorResult struct {
	Value []json.RawMessage `json:"value"`
}

// firstResultValue extracts the actual measured number Prometheus returned
// for the first result of a vector-typed query — VERDICT.md §1's
// "observed": "4218ms" is a real measured value, not a restatement of
// pass/fail, and this is the promql-path equivalent of findings.go's
// measuredValue for k6 thresholds. ok is false for any shape this cannot
// confidently read (a scalar/matrix result type, or a malformed value
// pair) — the caller falls back to reporting the result count instead of
// fabricating a number.
func firstResultValue(pr promResponse) (string, bool) {
	if pr.Data.ResultType != "vector" || len(pr.Data.Result) == 0 {
		return "", false
	}
	var vr promVectorResult
	if err := json.Unmarshal(pr.Data.Result[0], &vr); err != nil || len(vr.Value) != 2 {
		return "", false
	}
	var value string
	if err := json.Unmarshal(vr.Value[1], &value); err != nil {
		return "", false
	}
	return value, true
}

// Query implements PromQuerier.
func (q HTTPPromQuerier) Query(expr string) (bool, string, error) {
	reqURL := q.BaseURL + "/api/v1/query?" + url.Values{"query": {expr}}.Encode()
	resp, err := q.client().Get(reqURL)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, "", err
	}
	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return false, "", fmt.Errorf("run: promql: invalid response: %w", err)
	}
	if pr.Status != "success" {
		return false, "", fmt.Errorf("run: promql: query failed: %s", pr.Error)
	}
	holds := len(pr.Data.Result) > 0
	if !holds {
		return false, "no results (condition did not hold)", nil
	}
	if v, ok := firstResultValue(pr); ok {
		return true, v, nil
	}
	// Non-vector result type (scalar/matrix): still holds, but this
	// package does not confidently parse a single value out of that shape
	// — report the count rather than guess at one.
	return true, fmt.Sprintf("%d result(s)", len(pr.Data.Result)), nil
}
