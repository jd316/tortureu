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

// spec: R-DET-16
//
// The container side is what a base URL needs: k6 runs inside the SUT's own
// network namespace (internal/run/load.go), where the published host port
// is not bound at all. The E1 control case is the live example — it
// publishes 8081:8080, and its committed torture.yaml says 8080.
func TestSUTPortIsRecordedFromTheContainerSideNotThePublishedHostSide(t *testing.T) {
	path := writeCompose(t, `
services:
  api:
    build: .
    ports: ["8081:8080"]
  db:
    image: postgres:16
`)

	sys, err := detect.Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if got, want := sys.SUT, "api"; got != want {
		t.Fatalf("SUT = %q, want %q", got, want)
	}
	if got, want := strings.Join(sys.SUTPorts, ","), "8080"; got != want {
		t.Errorf("SUTPorts = %q, want %q", got, want)
	}
	// R-DET-4 must not regress: the dependency keeps its container-side
	// address, derived independently of this.
	if len(sys.Deps) != 1 || sys.Deps[0].Address != "db:5432" {
		t.Errorf("dependency address = %+v, want db:5432", sys.Deps)
	}
}

// spec: R-DET-16
func TestSUTWithSeveralDeclaredPortsRecordsAllOfThemDeduplicated(t *testing.T) {
	path := writeCompose(t, `
services:
  api:
    build: .
    ports:
      - "80:80"
      - "9229:9229"
      - "9230:9230"
    expose:
      - "9230"
`)

	sys, err := detect.Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	// 9230 is both published and exposed: one port, not two — otherwise a
	// service with a single obvious answer looks ambiguous to R-CLI-19.
	if got, want := strings.Join(sys.SUTPorts, ","), "80,9229,9230"; got != want {
		t.Errorf("SUTPorts = %q, want %q", got, want)
	}
}

// spec: R-DET-16
//
// An exposed port is unreachable from the host and perfectly reachable
// inside the namespace k6 joins, so it is a real candidate — this is the
// react-express-mongodb shape, whose backend exposes 3000 and publishes
// nothing.
func TestSUTExposedButUnpublishedPortIsRecorded(t *testing.T) {
	path := writeCompose(t, `
services:
  api:
    build: .
    expose:
      - "3000"
`)

	sys, err := detect.Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if got, want := strings.Join(sys.SUTPorts, ","), "3000"; got != want {
		t.Errorf("SUTPorts = %q, want %q", got, want)
	}
}

// spec: R-DET-16
//
// A udp listener is not an HTTP base URL, so it is dropped. A port range is
// not: compose-go expands it into its individual members, and each is a
// real candidate that R-CLI-19's several-ports refusal then names — which
// is the honest outcome, since compose does not say which member serves the
// API.
func TestSUTUDPPortIsDroppedAndAPortRangeBecomesIndividualCandidates(t *testing.T) {
	path := writeCompose(t, `
services:
  api:
    build: .
    ports:
      - "5514:5514/udp"
      - "8000-8002:8000-8002"
`)

	sys, err := detect.Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if got, want := strings.Join(sys.SUTPorts, ","), "8000,8001,8002"; got != want {
		t.Errorf("SUTPorts = %q, want %q", got, want)
	}
}

// spec: R-DET-16
//
// A bare "8000" gets an ephemeral *host* port at up time, but the
// container port it names is known and is the one that matters.
func TestSUTPortWithNoHostMappingIsStillRecordedFromItsContainerSide(t *testing.T) {
	path := writeCompose(t, `
services:
  api:
    build: .
    ports:
      - "8000"
`)

	sys, err := detect.Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if got, want := strings.Join(sys.SUTPorts, ","), "8000"; got != want {
		t.Errorf("SUTPorts = %q, want %q", got, want)
	}
}
