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

// spec: R-DET-19
//
// The nginx-nodejs-redis shape: three build services, one of them the proxy
// every request enters through. Alphabetical order picked "web2" — one of
// two identical backends behind the proxy.
func TestSUTAmongSeveralBuildServicesIsTheOneNoOtherServiceDependsOn(t *testing.T) {
	path := writeCompose(t, `
services:
  nginx:
    build: ./nginx
    ports: ["80:80"]
    depends_on:
      - web1
      - web2
  web1:
    build: ./web
  web2:
    build: ./web
  redis:
    image: redis:7
`)

	sys, err := detect.Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got, want := sys.SUT, "nginx"; got != want {
		t.Errorf("SUT = %q, want %q (nothing depends on it — load enters the graph there)", got, want)
	}
	if got, want := sys.SUTChoice, detect.SUTChoiceDerived; got != want {
		t.Errorf("SUTChoice = %q, want %q — a derived pick must be visible as one", got, want)
	}
	if len(sys.SUTCandidates) != 3 {
		t.Errorf("SUTCandidates = %v, want all three build services named", sys.SUTCandidates)
	}
}

// spec: R-DET-19
//
// The example-voting-app / react-rust-postgres shape: two build services,
// neither depending on the other, so the graph does not say which serves the
// traffic. Alphabetical order named voting-app's `worker` — a background
// consumer that publishes no port and cannot serve a request at all.
func TestSUTIsNotChosenWhenSeveralBuildServicesAreEquallyUnDependedOn(t *testing.T) {
	path := writeCompose(t, `
services:
  frontend:
    build: ./frontend
    ports: ["3000:3000"]
  backend:
    build: ./backend
    ports: ["8000:8000"]
    depends_on:
      - db
  db:
    image: postgres:16
`)

	sys, err := detect.Detect(path)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.SUT != "" {
		t.Errorf("SUT = %q, want none chosen — a wrong pick tortures the wrong container", sys.SUT)
	}
	if got, want := sys.SUTChoice, detect.SUTChoiceUndecided; got != want {
		t.Errorf("SUTChoice = %q, want %q", got, want)
	}
	if got, want := strings.Join(sys.SUTCandidates, ","), "backend,frontend"; got != want {
		t.Errorf("SUTCandidates = %q, want %q", got, want)
	}
	var gap string
	for _, g := range sys.Gaps {
		if strings.Contains(g, "system under test") {
			gap = g
		}
	}
	if gap == "" {
		t.Fatalf("gaps = %v, want one naming the undecided SUT", sys.Gaps)
	}
	for _, want := range []string{"backend", "frontend", "-service"} {
		if !strings.Contains(gap, want) {
			t.Errorf("gap %q does not name %s", gap, want)
		}
	}
	// Nothing is derived from a SUT that was not chosen.
	if len(sys.SUTPorts) != 0 {
		t.Errorf("SUTPorts = %v, want none — no SUT was chosen", sys.SUTPorts)
	}
}

// spec: R-DET-19
func TestCallerNamedSUTWinsOverTheDerivedChoice(t *testing.T) {
	path := writeCompose(t, `
services:
  frontend:
    build: ./frontend
    ports: ["3000:3000"]
  backend:
    build: ./backend
    ports: ["8000:8000"]
`)

	sys, err := detect.DetectWithSUT(path, "backend")
	if err != nil {
		t.Fatalf("DetectWithSUT: %v", err)
	}
	if got, want := sys.SUT, "backend"; got != want {
		t.Fatalf("SUT = %q, want %q", got, want)
	}
	if got, want := sys.SUTChoice, detect.SUTChoiceRequested; got != want {
		t.Errorf("SUTChoice = %q, want %q", got, want)
	}
	// The named service's own ports are what base_url derives from.
	if got, want := strings.Join(sys.SUTPorts, ","), "8000"; got != want {
		t.Errorf("SUTPorts = %q, want %q", got, want)
	}
	// R-DET-2 must not regress: the service that was not chosen is a build
	// service, so it is still neither a dependency nor a gap.
	if len(sys.Deps) != 0 {
		t.Errorf("deps = %+v, want none — frontend builds, so it is not a dependency", sys.Deps)
	}
	if len(sys.Gaps) != 0 {
		t.Errorf("gaps = %v, want none — the caller settled the ambiguity", sys.Gaps)
	}
}

// spec: R-DET-19
//
// A name that is not in the compose file must fail loudly: silently falling
// back to a derived pick would run the whole thing against something the
// caller did not ask for, having been told nothing.
func TestCallerNamedSUTThatIsNotAComposeServiceIsAnErrorNamingTheRealOnes(t *testing.T) {
	path := writeCompose(t, `
services:
  frontend:
    build: ./frontend
  backend:
    build: ./backend
`)

	_, err := detect.DetectWithSUT(path, "bakcend")
	if err == nil {
		t.Fatal("DetectWithSUT accepted a service that is not in the compose file")
	}
	for _, want := range []string{"bakcend", "backend", "frontend"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

// spec: R-DET-19
//
// The overwhelming case stays silent: one build service is the SUT with
// nothing reported, exactly as R-DET-8 had it.
func TestSingleBuildServiceIsChosenWithNothingReported(t *testing.T) {
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
	if got, want := sys.SUT, "api"; got != want {
		t.Fatalf("SUT = %q, want %q", got, want)
	}
	if sys.SUTChoice != detect.SUTChoiceOnly {
		t.Errorf("SUTChoice = %q, want %q", sys.SUTChoice, detect.SUTChoiceOnly)
	}
	if len(sys.SUTCandidates) != 0 {
		t.Errorf("SUTCandidates = %v, want none — there was nothing to choose between", sys.SUTCandidates)
	}
	if len(sys.Gaps) != 0 {
		t.Errorf("gaps = %v, want none", sys.Gaps)
	}
}
