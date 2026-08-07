package run

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// spec: R-CFG-17
func TestHTTPPromQuerier_NonEmptyResultMeansConditionHolds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != `sum(rate(app_retries_total[30s])) < 100` {
			t.Errorf("query = %q", r.URL.Query().Get("query"))
		}
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}`))
	}))
	defer srv.Close()

	q := HTTPPromQuerier{BaseURL: srv.URL}
	holds, _, err := q.Query(`sum(rate(app_retries_total[30s])) < 100`)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !holds {
		t.Error("holds = false, want true for a non-empty result vector")
	}
}

// spec: R-CFG-17
func TestHTTPPromQuerier_EmptyResultMeansConditionFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	q := HTTPPromQuerier{BaseURL: srv.URL}
	holds, observed, err := q.Query(`orders_total == payments_total`)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if holds {
		t.Error("holds = true, want false for an empty result vector")
	}
	if observed == "" {
		t.Error("observed is empty, want a human-readable rendering of what was measured (R-VER-5)")
	}
}

// spec: R-CFG-17
func TestHTTPPromQuerier_ServerErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"error","error":"bad query"}`))
	}))
	defer srv.Close()

	q := HTTPPromQuerier{BaseURL: srv.URL}
	if _, _, err := q.Query(`invalid{`); err == nil {
		t.Error("Query returned nil error for a Prometheus-reported query failure")
	}
}
