package detect_test

import (
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
)

// spec: R-DET-4
func TestExternalHostFromEnvironmentIsUnclassified(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
    environment:
      STRIPE_URL: "https://api.stripe.com/v1"
  db:
    image: postgres:16
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	found := false
	for _, h := range sys.Egress {
		if h == "api.stripe.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Egress = %v, want it to contain api.stripe.com", sys.Egress)
	}
	if got, want := sys.EgressClass["api.stripe.com"], "unclassified"; got != want {
		t.Errorf("EgressClass[api.stripe.com] = %q, want %q", got, want)
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
// is its own harm.
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
			t.Errorf("Egress = %v contains %q, want only the postgres dependency (ambiguous values must not be guessed)", sys.Egress, h)
		}
	}
}
