// Package doc lives in scrub.go; this file is the capturing proxy.
package capture

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// hopByHopHeaders are stripped when forwarding, per RFC 7230 §6.1 — the
// same set net/http/httputil.ReverseProxy excludes.
var hopByHopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding",
	"TE", "Trailer", "Upgrade", "Proxy-Authenticate", "Proxy-Authorization",
}

// Recorder is a capturing HTTP proxy: it forwards every request to Upstream
// unmodified, returns the real response to the caller, and — after
// scrubbing (R-DC2-5) — appends the exchange to Out as a cassette.Entry.
//
// This is the "proxy TortureU already controls" capture point the task
// brief pointed at (DC-2's Toxiproxy is architecturally the same idea: a
// proxy in the traffic path that can observe without the SUT knowing). It
// does not reuse ToxiproxyApplier directly — that type drives Toxiproxy's
// *control* API (creating proxies/toxics on a running Toxiproxy container
// inside a Docker-orchestrated run) and Toxiproxy itself has no built-in
// "write what passed through me to a file" capability, so there is nothing
// on that API surface to call for capture. Recorder is a small, standalone
// proxy with the same shape — sits in the path, doesn't touch payloads
// except to scrub before persisting — usable both inside a DC-2 run (point
// a fault's target at it) and standalone (`tortureu capture -upstream
// <url>`) without requiring Docker, a compose file, or root/eBPF.
type Recorder struct {
	// Upstream is the real destination every request is forwarded to.
	Upstream *url.URL
	// Out receives one scrubbed JSONL line per completed exchange. Nothing
	// is ever written to Out before Scrub{Header,Body} has run on it — see
	// ServeHTTP.
	Out io.Writer
	// Client sends the upstream request; defaults to http.DefaultClient.
	Client *http.Client
	// ErrOut receives a line for any entry that failed to write to Out
	// (e.g. a full disk). It is never the SUT-visible response — that has
	// already been written by the time a write to Out is attempted — but a
	// dropped capture must be visible somewhere rather than silently
	// vanishing. Defaults to io.Discard.
	ErrOut io.Writer

	mu    sync.Mutex // serializes Out writes, seq increments and epoch init
	seq   int
	count int
	// epoch is the origin of the cassette's monotonic timeline (R-CLI-13),
	// fixed on the first request this Recorder serves. Every Entry's
	// CallNS/ReturnNS is a duration from it, so instants are comparable
	// within one cassette and carry no wall-clock meaning.
	epoch time.Time
}

// since returns nanoseconds from this Recorder's epoch, fixing the epoch on
// first use. time.Time carries a monotonic reading, so Sub is immune to a
// wall-clock step — which is the whole point: a clock jump mid-capture
// must not reorder operations that did not reorder.
func (rec *Recorder) since(t time.Time) int64 {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.epoch.IsZero() {
		rec.epoch = t
	}
	// A nonzero floor: 0 is the "no instants recorded" value a
	// pre-R-CLI-13 cassette decodes to, so the very first call must not
	// be indistinguishable from it (see Entry.HasHistory).
	return t.Sub(rec.epoch).Nanoseconds() + 1
}

// Count returns how many exchanges have been captured so far.
func (rec *Recorder) Count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.count
}

func (rec *Recorder) client() *http.Client {
	if rec.Client != nil {
		return rec.Client
	}
	return http.DefaultClient
}

func stripHopByHop(h http.Header) {
	for _, k := range hopByHopHeaders {
		h.Del(k)
	}
}

// ServeHTTP implements http.Handler: forward, capture, scrub, persist.
func (rec *Recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	start := time.Now()
	callNS := rec.since(start)

	reqBody, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "capture: read request body: "+err.Error(), http.StatusBadGateway)
		return
	}
	req.Body.Close()

	target := *rec.Upstream
	target.Path = req.URL.Path
	target.RawQuery = req.URL.RawQuery

	outReq, err := http.NewRequest(req.Method, target.String(), bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "capture: build upstream request: "+err.Error(), http.StatusBadGateway)
		return
	}
	outReq.Header = req.Header.Clone()
	stripHopByHop(outReq.Header)

	resp, err := rec.client().Do(outReq)
	if err != nil {
		http.Error(w, "capture: upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "capture: read upstream response: "+err.Error(), http.StatusBadGateway)
		return
	}

	respHeader := resp.Header.Clone()
	stripHopByHop(respHeader)
	for k, vs := range respHeader {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)

	end := time.Now()
	elapsed := end.Sub(start)
	returnNS := rec.since(end)

	// Scrubbing happens here, before anything reaches WriteEntry — the
	// R-DC2-5 boundary this whole package exists to hold. See
	// recorder_test.go's TestRecorderNeverWritesCredentialToDisk.
	scrubbedReqHeader := ScrubHeader(req.Header)
	scrubbedRespHeader := ScrubHeader(respHeader)
	scrubbedReqBody := ScrubBody(reqBody)
	scrubbedRespBody := ScrubBody(respBody)
	scrubbedURL := ScrubURL(req.URL.RequestURI())

	bodyText, bodyEnc := encodeBody(scrubbedReqBody)
	respBodyText, respBodyEnc := encodeBody(scrubbedRespBody)

	rec.mu.Lock()
	rec.seq++
	entry := Entry{
		Seq:              rec.seq,
		Method:           req.Method,
		URL:              scrubbedURL,
		Header:           map[string][]string(scrubbedReqHeader),
		Body:             bodyText,
		BodyEncoding:     bodyEnc,
		Status:           resp.StatusCode,
		RespHeader:       map[string][]string(scrubbedRespHeader),
		RespBody:         respBodyText,
		RespBodyEncoding: respBodyEnc,
		DurationMS:       elapsed.Milliseconds(),
		CallNS:           callNS,
		ReturnNS:         returnNS,
	}
	writeErr := WriteEntry(rec.Out, entry)
	if writeErr == nil {
		rec.count++
	}
	rec.mu.Unlock()

	if writeErr != nil {
		// The response has already been written to the real client by this
		// point (headers/status can't be changed after w.WriteHeader), so a
		// capture failure can never become an SUT-visible failure — but it
		// MUST be visible somewhere rather than silently dropped.
		errOut := rec.ErrOut
		if errOut == nil {
			errOut = io.Discard
		}
		fmt.Fprintf(errOut, "capture: write entry %d: %v\n", entry.Seq, writeErr)
	}
}
