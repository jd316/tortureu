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
	return http.DefaultClient
}

type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string            `json:"resultType"`
		Result     []json.RawMessage `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
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
	observed := fmt.Sprintf("%d result(s)", len(pr.Data.Result))
	if !holds {
		observed = "no results (condition did not hold)"
	}
	return holds, observed, nil
}
