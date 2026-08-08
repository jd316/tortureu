package capture

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

const recorderKnownSecret = "sk_live_51H8x9secretDONOTLEAK"

// spec: R-CLI-9 (proposed)
// spec: R-DC2-5
//
// This is the test that matters most in this task: it proves a known
// credential — sent as both an Authorization header and a JSON body field —
// never reaches the cassette file on disk, by reading the actual bytes
// os.Open returns rather than inspecting an in-memory Entry.
func TestRecorderNeverWritesCredentialToDisk(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session="+recorderKnownSecret)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"token":"` + recorderKnownSecret + `"}`))
	}))
	defer upstream.Close()
	upstreamURL, _ := url.Parse(upstream.URL)

	dir := t.TempDir()
	cassettePath := dir + "/cassette.jsonl"
	f, err := os.Create(cassettePath)
	if err != nil {
		t.Fatalf("create cassette: %v", err)
	}

	rec := &Recorder{Upstream: upstreamURL, Out: f}
	proxy := httptest.NewServer(rec)
	defer proxy.Close()

	req, _ := http.NewRequest(http.MethodPost, proxy.URL+"/login",
		strings.NewReader(`{"user":"joe","password":"`+recorderKnownSecret+`"}`))
	req.Header.Set("Authorization", "Bearer "+recorderKnownSecret)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// The real response must still reach the caller unmodified — capture is
	// observational, not a filter on the SUT's own traffic.
	if !strings.Contains(string(respBody), recorderKnownSecret) {
		t.Fatalf("proxy must forward the real, unscrubbed response to the caller; got %s", respBody)
	}

	if err := f.Close(); err != nil {
		t.Fatalf("close cassette: %v", err)
	}

	onDisk, err := os.ReadFile(cassettePath)
	if err != nil {
		t.Fatalf("read cassette file: %v", err)
	}

	if strings.Contains(string(onDisk), recorderKnownSecret) {
		t.Fatalf("R-DC2-5 VIOLATED: known credential found in cassette bytes on disk:\n%s", onDisk)
	}
	if !strings.Contains(string(onDisk), Redacted) {
		t.Errorf("cassette on disk has no %q marker at all — scrubbing may not have run: %s", Redacted, onDisk)
	}
}

// spec: R-CLI-9 (proposed)
func TestRecorderCapturesAndCountsExchange(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	upstreamURL, _ := url.Parse(upstream.URL)

	var buf strings.Builder
	rec := &Recorder{Upstream: upstreamURL, Out: &buf}
	proxy := httptest.NewServer(rec)
	defer proxy.Close()

	resp, err := http.Get(proxy.URL + "/anything")
	if err != nil {
		t.Fatalf("GET through proxy: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want 418 (forwarded from upstream)", resp.StatusCode)
	}
	if rec.Count() != 1 {
		t.Errorf("Count() = %d, want 1", rec.Count())
	}
	entries, err := ReadCassette(strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("ReadCassette: %v", err)
	}
	if len(entries) != 1 || entries[0].Status != http.StatusTeapot {
		t.Errorf("entries = %+v, want one entry with status 418", entries)
	}
}
