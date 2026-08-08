package capture

import (
	"net/http"
	"strings"
	"testing"
)

const knownSecret = "sk_live_51H8x9secretDONOTLEAK"

// spec: R-CLI-9 (proposed)
func TestScrubHeaderRedactsAuthorization(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+knownSecret)
	h.Set("Content-Type", "application/json")

	out := ScrubHeader(h)

	if strings.Contains(out.Get("Authorization"), knownSecret) {
		t.Fatalf("Authorization header still contains the secret: %q", out.Get("Authorization"))
	}
	if out.Get("Content-Type") != "application/json" {
		t.Errorf("non-sensitive header was altered: %q", out.Get("Content-Type"))
	}
}

// spec: R-CLI-9 (proposed)
func TestScrubHeaderRedactsCookie(t *testing.T) {
	h := http.Header{}
	h.Set("Cookie", "session="+knownSecret)

	out := ScrubHeader(h)

	if strings.Contains(out.Get("Cookie"), knownSecret) {
		t.Fatalf("Cookie header still contains the secret: %q", out.Get("Cookie"))
	}
}

// spec: R-CLI-9 (proposed)
func TestScrubBodyRedactsJSONPasswordField(t *testing.T) {
	body := []byte(`{"user":"joe","password":"` + knownSecret + `"}`)

	out := ScrubBody(body)

	if strings.Contains(string(out), knownSecret) {
		t.Fatalf("scrubbed JSON body still contains the secret: %s", out)
	}
	if !strings.Contains(string(out), `"user":"joe"`) {
		t.Errorf("non-sensitive JSON field was altered: %s", out)
	}
}

// spec: R-CLI-9 (proposed)
func TestScrubBodyRedactsNestedJSONField(t *testing.T) {
	body := []byte(`{"auth":{"api_key":"` + knownSecret + `"},"ok":true}`)

	out := ScrubBody(body)

	if strings.Contains(string(out), knownSecret) {
		t.Fatalf("scrubbed nested JSON body still contains the secret: %s", out)
	}
}

// spec: R-CLI-9 (proposed)
func TestScrubBodyRedactsFormEncodedField(t *testing.T) {
	body := []byte("username=joe&password=" + knownSecret)

	out := ScrubBody(body)

	if strings.Contains(string(out), knownSecret) {
		t.Fatalf("scrubbed form body still contains the secret: %s", out)
	}
}

// spec: R-CLI-9 (proposed)
func TestScrubBodyRedactsBearerTokenInPlainText(t *testing.T) {
	body := []byte("curl -H 'Authorization: Bearer " + knownSecret + "' https://api.example.com")

	out := ScrubBody(body)

	if strings.Contains(string(out), knownSecret) {
		t.Fatalf("scrubbed plain-text body still contains the secret: %s", out)
	}
}

// spec: R-CLI-9 (proposed)
func TestScrubURLRedactsSensitiveQueryParam(t *testing.T) {
	out := ScrubURL("/reset?token=" + knownSecret + "&user=joe")

	if strings.Contains(out, knownSecret) {
		t.Fatalf("scrubbed URL still contains the secret: %s", out)
	}
	if !strings.Contains(out, "user=joe") {
		t.Errorf("non-sensitive query param was altered: %s", out)
	}
}

// spec: R-CLI-9 (proposed)
func TestScrubBodyLeavesNonUTF8Untouched(t *testing.T) {
	body := []byte{0xff, 0xfe, 0x00, 0x01}

	out := ScrubBody(body)

	if len(out) != len(body) {
		t.Fatalf("binary body was mutated: got %v, want %v", out, body)
	}
}
