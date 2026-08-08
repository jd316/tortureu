// Package capture implements the `capture` and `replay` verbs (R-CLI-9,
// R-CLI-10): recording HTTP traffic through a small proxy TortureU controls
// and later driving a saved cassette back as load.
//
// R-DC2-5 governs this package's reason for existing in its current shape:
// "Captured traffic MUST be secret-scrubbed on write. Scrubbing on replay
// only is non-compliant." Scrub (this file) runs inside Recorder.ServeHTTP
// (recorder.go) BEFORE cassette.WriteEntry ever sees the bytes — there is
// no code path in this package that writes an Entry without having called
// Scrub first. See recorder_test.go's
// TestRecorderNeverWritesCredentialToDisk for the test that proves it.
package capture

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"unicode/utf8"
)

// Redacted replaces any credential-shaped value this package finds.
const Redacted = "[SCRUBBED]"

// sensitiveHeaders are canonicalized (http.CanonicalHeaderKey) header names
// scrubbed unconditionally: their entire value is credential-shaped by
// definition, never just part of it.
var sensitiveHeaders = map[string]bool{
	"Authorization":        true,
	"Proxy-Authorization":  true,
	"Cookie":               true,
	"Set-Cookie":           true,
	"X-Api-Key":            true,
	"X-Auth-Token":         true,
	"X-Access-Token":       true,
	"X-Csrf-Token":         true,
	"X-Session-Token":      true,
	"X-Amz-Security-Token": true,
}

// sensitiveFields are JSON object keys / form field names scrubbed
// case-insensitively wherever they appear, at any nesting depth.
var sensitiveFields = map[string]bool{
	"password":      true,
	"passwd":        true,
	"pass":          true,
	"secret":        true,
	"token":         true,
	"access_token":  true,
	"refresh_token": true,
	"id_token":      true,
	"api_key":       true,
	"apikey":        true,
	"client_secret": true,
	"private_key":   true,
	"authorization": true,
	"session":       true,
	"ssn":           true,
	"credit_card":   true,
}

// bearerRE catches an Authorization-style bearer token embedded inside a
// body or URL rather than carried in its own header (e.g. a form field or a
// query string a client builds by hand).
var bearerRE = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-_.=]+`)

// kvFieldRE catches "key=value" pairs (query strings, form-encoded bodies)
// whose key names a sensitive field, e.g. "password=hunter2" or
// "api_key=abc123" inside raw, non-JSON text.
var kvFieldRE = regexp.MustCompile(`(?i)\b(password|passwd|pass|secret|token|access_token|refresh_token|api_key|apikey|client_secret|private_key)=[^&\s]+`)

// ScrubHeader returns a copy of h with every sensitive header's values
// replaced by Redacted. h is not mutated.
func ScrubHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		if sensitiveHeaders[http.CanonicalHeaderKey(k)] {
			out[k] = []string{Redacted}
			continue
		}
		cp := make([]string, len(vs))
		copy(cp, vs)
		out[k] = cp
	}
	return out
}

// ScrubURL returns u's RequestURI with any sensitive query parameter's
// value replaced by Redacted. Malformed input is returned unchanged rather
// than dropped — capture must never fabricate data, only redact it.
func ScrubURL(requestURI string) string {
	u, err := url.Parse(requestURI)
	if err != nil {
		return requestURI
	}
	q := u.Query()
	if len(q) == 0 {
		return requestURI
	}
	changed := false
	for k := range q {
		if sensitiveFields[normalizeKey(k)] {
			q[k] = []string{Redacted}
			changed = true
		}
	}
	if !changed {
		return requestURI
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ScrubBody scrubs a request or response body. It is the last line of
// defence, not the only one: headers are handled by ScrubHeader before this
// is ever reached, but a body can carry the same shape of secret a header
// does (a JSON "password" field, a bearer token quoted in a form field), so
// it gets its own pass rather than trusting header-scrubbing alone.
//
// Non-UTF8 (binary) bodies are not text-scanned — there is no credential
// pattern to match against opaque bytes — and are returned unchanged; the
// caller (cassette writer) is responsible for base64-encoding them for the
// cassette rather than writing raw bytes into a text format.
func ScrubBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	if !utf8.Valid(body) {
		return body
	}
	if scrubbed, ok := scrubJSON(body); ok {
		return scrubbed
	}
	out := bearerRE.ReplaceAll(body, []byte("Bearer "+Redacted))
	out = kvFieldRE.ReplaceAll(out, []byte("$1="+Redacted))
	return out
}

// scrubJSON attempts to parse body as JSON and redact sensitive fields at
// any depth. ok is false when body is not JSON at all, so ScrubBody can
// fall back to the regex passes instead.
func scrubJSON(body []byte) ([]byte, bool) {
	var v interface{}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	scrubValue(v)
	out, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return out, true
}

func scrubValue(v interface{}) {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, val := range t {
			if sensitiveFields[normalizeKey(k)] {
				t[k] = Redacted
				continue
			}
			scrubValue(val)
		}
	case []interface{}:
		for _, e := range t {
			scrubValue(e)
		}
	}
}

func normalizeKey(k string) string {
	out := make([]byte, 0, len(k))
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}
