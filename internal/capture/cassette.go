package capture

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

// Entry is one recorded request/response pair. The cassette format is one
// JSON object per line (JSONL): human-readable and `diff`-able, so a
// reviewer can `git diff` a cassette the way they would any other checked-in
// fixture — the design note in the task brief calling for "a corpus a human
// can read and diff" is why this isn't a binary pcap or protobuf stream.
//
// Every byte in Body/RespBody has already passed through ScrubBody (and
// every header through ScrubHeader) by the time an Entry is constructed —
// see recorder.go. Body/RespBody are UTF-8 text unless BodyEncoding /
// RespBodyEncoding is "base64" (set only when the original bytes were not
// valid UTF-8 — see scrub.go's ScrubBody doc comment on why binary bodies
// bypass the text scrub passes).
type Entry struct {
	Seq              int                 `json:"seq"`
	Method           string              `json:"method"`
	URL              string              `json:"url"`
	Header           map[string][]string `json:"header,omitempty"`
	Body             string              `json:"body,omitempty"`
	BodyEncoding     string              `json:"body_encoding,omitempty"`
	Status           int                 `json:"status"`
	RespHeader       map[string][]string `json:"resp_header,omitempty"`
	RespBody         string              `json:"resp_body,omitempty"`
	RespBodyEncoding string              `json:"resp_body_encoding,omitempty"`
	DurationMS       int64               `json:"duration_ms"`

	// CallNS and ReturnNS are the exchange's absolute call and return
	// instants (R-CLI-13): integer nanoseconds on a single monotonic
	// timeline whose origin is the start of the recording session. They
	// are comparable only against other entries in the SAME cassette, and
	// are deliberately not wall-clock — a clock step mid-capture would
	// reorder operations that did not reorder.
	//
	// They exist because a linearizability check is defined entirely by
	// which operations overlapped in real time, and Seq cannot express
	// overlap. DurationMS stays even though ReturnNS-CallNS derives it:
	// it is what a human reads in a `git diff` of a cassette.
	//
	// Both are additive. A cassette written before R-CLI-13 has neither,
	// which decodes as 0/0 — see HasHistory, and never read that as "the
	// exchange happened at time zero".
	CallNS   int64 `json:"call_ns,omitempty"`
	ReturnNS int64 `json:"return_ns,omitempty"`
}

// HasHistory reports whether this entry carries the R-CLI-13 call/return
// instants a real-time-overlap consumer (a linearizability checker) needs.
// A pre-R-CLI-13 cassette answers false, which is the R-COV-6 "cannot
// evaluate" case, not "no overlap".
func (e Entry) HasHistory() bool { return e.ReturnNS > e.CallNS && e.CallNS > 0 }

// encodeBody turns already-scrubbed bytes into an Entry's text field plus
// its encoding marker.
func encodeBody(b []byte) (text, encoding string) {
	if len(b) == 0 {
		return "", ""
	}
	if utf8.Valid(b) {
		return string(b), ""
	}
	return base64.StdEncoding.EncodeToString(b), "base64"
}

// decodeBody reverses encodeBody.
func decodeBody(text, encoding string) ([]byte, error) {
	if text == "" {
		return nil, nil
	}
	if encoding == "base64" {
		return base64.StdEncoding.DecodeString(text)
	}
	return []byte(text), nil
}

// RequestBody returns e.Body decoded back to raw bytes.
func (e Entry) RequestBody() ([]byte, error) { return decodeBody(e.Body, e.BodyEncoding) }

// ResponseBody returns e.RespBody decoded back to raw bytes.
func (e Entry) ResponseBody() ([]byte, error) { return decodeBody(e.RespBody, e.RespBodyEncoding) }

// WriteEntry appends e to w as one JSONL line. Callers MUST have already
// scrubbed e's headers and bodies (R-DC2-5) — this function has no
// knowledge of what scrubbing is and cannot enforce it; recorder.go is the
// single call site and does the scrubbing immediately before calling this.
func WriteEntry(w io.Writer, e Entry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("capture: marshal entry: %w", err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("capture: write entry: %w", err)
	}
	return nil
}

// ReadCassette reads every Entry from a JSONL cassette, in file order.
func ReadCassette(r io.Reader) ([]Entry, error) {
	var entries []Entry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("capture: parse cassette line: %w", err)
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("capture: read cassette: %w", err)
	}
	return entries, nil
}
