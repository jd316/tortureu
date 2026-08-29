package detect_test

import (
	"testing"

	"github.com/jd316/tortureu/internal/detect"
)

// spec: R-DET-4
// Covers all three true-positive shapes discovery must still recognize
// after the filename-tightening fix: a scheme URL, a bare host:port, and a
// bare host with no port at all.
func TestExternalHostFromEnvironmentIsUnclassified(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
    environment:
      STRIPE_URL: "https://api.stripe.com/v1"
      TWILIO_HOST: "api.twilio.com:443"
      PARTNER_HOST: "api.partner.com"
  db:
    image: postgres:16
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	for _, want := range []string{"api.stripe.com", "api.twilio.com:443", "api.partner.com"} {
		found := false
		for _, h := range sys.Egress {
			if h == want {
				found = true
			}
		}
		if !found {
			t.Errorf("Egress = %v, want it to contain %q", sys.Egress, want)
			continue
		}
		if got := sys.EgressClass[want]; got != "unclassified" {
			t.Errorf("EgressClass[%q] = %q, want %q", want, got, "unclassified")
		}
	}
}

// spec: R-DET-4
func TestExternalHostFromEnvFileIsUnclassified(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
    env_file:
      - api.env
  db:
    image: postgres:16
`)
	writeFile(t, dir, "api.env", `
# partner API
PARTNER_HOST=api.partner.com:8443
LOG_LEVEL=debug
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	found := false
	for _, h := range sys.Egress {
		if h == "api.partner.com:8443" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Egress = %v, want it to contain api.partner.com:8443", sys.Egress)
	}
	if got, want := sys.EgressClass["api.partner.com:8443"], "unclassified"; got != want {
		t.Errorf("EgressClass[api.partner.com:8443] = %q, want %q", got, want)
	}
}

// spec: R-DET-4
// An env var value that merely names another compose service (an intra-
// stack reference resolved by Docker's own DNS) MUST stay internal, never
// be reclassified as an external, unclassified host.
func TestEnvironmentReferenceToInComposeServiceStaysInternal(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
    environment:
      REDIS_URL: "redis://cache:6379"
  cache:
    image: redis:7
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if got, want := sys.EgressClass["cache:6379"], "internal"; got != want {
		t.Errorf("EgressClass[cache:6379] = %q, want %q", got, want)
	}
	for host, class := range sys.EgressClass {
		if class == "unclassified" {
			t.Errorf("EgressClass[%q] = unclassified, want cache to never be reclassified via env var reference", host)
		}
	}
}

// spec: R-DET-4
// An ambiguous env var value (no scheme, not a dotted hostname) must not be
// guessed at — a false unclassified host blocks runs for no reason, which
// is its own harm. Extended after a real regression: a dotted filename
// (config.yaml, ca.pem, app.js, schema.sql, Dockerfile.prod) or a dotfile
// (.env) is exactly as ordinary in an env var as a real hostname is, and an
// over-permissive "ends in 2+ letters" check misclassified every one of
// them as an external host — which would make `tortureu run` abort on
// nearly any real repo.
func TestAmbiguousEnvironmentValueIsNotGuessedAsAHost(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
    environment:
      APP_VERSION: "3.11.4"
      LOG_LEVEL: "debug"
      REGION: "us-east-1"
      CONFIG_PATH: "config.yaml"
      TLS_CERT: "ca.pem"
      ENTRY_POINT: "app.js"
      SCHEMA_FILE: "schema.sql"
      DOTENV_PATH: ".env"
      BUILD_STAGE: "Dockerfile.prod"
  db:
    image: postgres:16
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	want := map[string]bool{"db:5432": true}
	for _, h := range sys.Egress {
		if !want[h] {
			t.Errorf("Egress = %v contains %q, want only the postgres dependency (ambiguous/filename-shaped values must not be guessed)", sys.Egress, h)
		}
	}
}
