package capture

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
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

// spec: R-CLI-13 (proposed)
//
// A cassette must carry each exchange's absolute call and return instants,
// read back from the file on disk (not from an in-memory Entry), because a
// linearizability checker needs real-time overlap and cannot get it from
// seq. Two overlapping requests must show overlapping [call_ns, return_ns]
// intervals — the fact that seq alone destroys.
func TestRecorderWritesCallAndReturnInstants(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Both requests are in flight together: each blocks until the
		// test releases them, so their intervals genuinely overlap.
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	upstreamURL, _ := url.Parse(upstream.URL)

	path := t.TempDir() + "/cassette.jsonl"
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create cassette: %v", err)
	}

	rec := &Recorder{Upstream: upstreamURL, Out: f}
	proxy := httptest.NewServer(rec)
	defer proxy.Close()

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(proxy.URL + "/x")
			if err != nil {
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if err := f.Close(); err != nil {
		t.Fatalf("close cassette: %v", err)
	}

	onDisk, err := os.Open(path)
	if err != nil {
		t.Fatalf("open cassette: %v", err)
	}
	defer onDisk.Close()
	entries, err := ReadCassette(onDisk)
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if e.CallNS <= 0 {
			t.Errorf("entry %d: call_ns = %d, want a positive instant", e.Seq, e.CallNS)
		}
		if e.ReturnNS <= e.CallNS {
			t.Errorf("entry %d: return_ns %d must be after call_ns %d", e.Seq, e.ReturnNS, e.CallNS)
		}
	}
	a, b := entries[0], entries[1]
	if !(a.CallNS < b.ReturnNS && b.CallNS < a.ReturnNS) {
		t.Errorf("concurrent exchanges must record overlapping intervals: "+
			"[%d,%d] and [%d,%d]", a.CallNS, a.ReturnNS, b.CallNS, b.ReturnNS)
	}

	// The raw bytes must carry the documented field names, since a
	// consumer outside this package reads the file, not the struct.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cassette bytes: %v", err)
	}
	for _, key := range []string{`"call_ns"`, `"return_ns"`, `"duration_ms"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("cassette on disk missing %s:\n%s", key, raw)
		}
	}
}

// spec: R-CLI-13 (proposed)
// spec: R-CLI-10 (proposed)
//
// A cassette written before R-CLI-13 has no instants. It must still read
// and still replay — the fields are additive, and an old cassette is
// neither misread nor refused.
func TestOldCassetteWithoutInstantsStillReads(t *testing.T) {
	old := `{"seq":1,"method":"GET","url":"/x","status":200,"duration_ms":3}` + "\n"
	entries, err := ReadCassette(strings.NewReader(old))
	if err != nil {
		t.Fatalf("ReadCassette: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].CallNS != 0 || entries[0].ReturnNS != 0 {
		t.Errorf("old cassette must yield zero instants, got %d/%d", entries[0].CallNS, entries[0].ReturnNS)
	}
	if entries[0].HasHistory() {
		t.Error("HasHistory() must be false for a cassette with no instants")
	}

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	u, _ := url.Parse(target.URL)
	res, err := Replay(entries, u, 1, nil)
	if err != nil {
		t.Fatalf("Replay of a pre-R-CLI-13 cassette: %v", err)
	}
	if res.Sent != 1 || res.Success != 1 {
		t.Errorf("Replay = %+v, want 1 sent / 1 success", res)
	}
}
