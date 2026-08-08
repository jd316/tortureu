package capture

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

// spec: R-CLI-10 (proposed)
func TestReplaySendsEachEntryRepeatTimes(t *testing.T) {
	var hits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	targetURL, _ := url.Parse(target.URL)

	entries := []Entry{
		{Seq: 1, Method: "GET", URL: "/a"},
		{Seq: 2, Method: "GET", URL: "/b"},
	}

	res, err := Replay(entries, targetURL, 3, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Sent != 6 {
		t.Errorf("Sent = %d, want 6 (2 entries x 3 repeat)", res.Sent)
	}
	if res.Success != 6 {
		t.Errorf("Success = %d, want 6", res.Success)
	}
	if got := atomic.LoadInt32(&hits); got != 6 {
		t.Errorf("target saw %d requests, want 6", got)
	}
}

// spec: R-CLI-10 (proposed)
func TestReplayCountsServerErrorsAsFailed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer target.Close()
	targetURL, _ := url.Parse(target.URL)

	res, err := Replay([]Entry{{Seq: 1, Method: "GET", URL: "/a"}}, targetURL, 1, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Failed != 1 || res.Success != 0 {
		t.Errorf("Result = %+v, want 1 failed, 0 success for a 500", res)
	}
}

// spec: R-CLI-10 (proposed)
func TestReplayRepeatBelowOneTreatedAsOne(t *testing.T) {
	var hits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	targetURL, _ := url.Parse(target.URL)

	res, err := Replay([]Entry{{Seq: 1, Method: "GET", URL: "/a"}}, targetURL, 0, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Sent != 1 {
		t.Errorf("Sent = %d, want 1 (repeat<1 clamped to 1)", res.Sent)
	}
}
