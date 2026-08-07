package detect_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
)

// writeCompose puts a compose file in a temp dir and returns its path.
func writeCompose(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// spec: R-DET-2
func TestPostgresImageIsDetectedAsPostgresqlDependency(t *testing.T) {
	path := writeCompose(t, `
services:
  api:
    build: .
    ports: ["8080:8080"]
  db:
    image: postgres:16
`)

	sys, err := detect.Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if len(sys.Deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(sys.Deps), sys.Deps)
	}
	if got, want := sys.Deps[0].Name, "db"; got != want {
		t.Errorf("dep name = %q, want %q", got, want)
	}
	if got, want := sys.Deps[0].Type, "postgresql"; got != want {
		t.Errorf("dep type = %q, want %q", got, want)
	}
}

// spec: R-DET-3
func TestUnrecognizedImageIsReportedAsGapNotGuessed(t *testing.T) {
	path := writeCompose(t, `
services:
  api:
    build: .
  weird:
    image: acme/some-internal-thing:v3
`)

	sys, err := detect.Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	// D-3: detection is capped at compose+lockfile, so unknowns must surface
	// as gaps. Guessing a type here would silently under-test the system.
	if len(sys.Gaps) != 1 {
		t.Fatalf("got %d gaps, want 1: %+v", len(sys.Gaps), sys.Gaps)
	}
	if !strings.Contains(sys.Gaps[0], "acme/some-internal-thing:v3") {
		t.Errorf("gap %q does not name the unrecognized image", sys.Gaps[0])
	}
}
