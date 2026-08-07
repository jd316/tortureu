// Package config_test drives internal/config through TDD, one requirement at a time.
package config_test

import (
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/config"
)

// spec: R-CFG-2
func TestUnknownTopLevelKeyIsError(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
asert:
  - http_req_duration: ["p(95)<500"]
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for unknown top-level key, got nil")
	}
	if !strings.Contains(err.Error(), "asert") {
		t.Fatalf("expected error to name the offending key %q, got: %v", "asert", err)
	}
}
