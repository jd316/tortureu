package detect_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
)

// writeFile writes body to path relative to dir, creating parent dirs as needed.
func writeFile(t *testing.T, dir, rel, body string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// spec: R-DET-8
func TestServiceWithBuildAndImageIsSUTNotDependency(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
    image: myregistry/api:latest
  db:
    image: postgres:16
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if sys.SUT != "api" {
		t.Errorf("SUT = %q, want %q", sys.SUT, "api")
	}
	for _, d := range sys.Deps {
		if d.Name == "api" {
			t.Errorf("service with build+image was classified as a dependency: %+v", d)
		}
	}
	if len(sys.Deps) != 1 || sys.Deps[0].Name != "db" {
		t.Errorf("deps = %+v, want just db", sys.Deps)
	}
}

// spec: R-DET-4
func TestEveryDependencyAddressIsWrittenToEgress(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if len(sys.Deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(sys.Deps), sys.Deps)
	}
	addr := sys.Deps[0].Address
	if addr == "" {
		t.Fatalf("dep %q has no address", sys.Deps[0].Name)
	}
	found := false
	for _, h := range sys.Egress {
		if h == addr {
			found = true
		}
	}
	if !found {
		t.Errorf("egress %v does not contain dependency address %q", sys.Egress, addr)
	}
}

// spec: R-DET-7
func TestGapsAreReturnedExplicitlyForEveryUnknown(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  weird1:
    image: acme/mystery-one:v1
  weird2:
    image: acme/mystery-two:v2
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if len(sys.Gaps) != 2 {
		t.Fatalf("got %d gaps, want 2 (one per unknown, explicit): %+v", len(sys.Gaps), sys.Gaps)
	}
}

// spec: R-DET-9
func TestDepTypeTableMapsImagesToVocabulary(t *testing.T) {
	cases := []struct {
		image string
		want  string
	}{
		{"redis:7", "redis"},
		{"mysql:8", "mysql"},
		{"bitnami/kafka:3", "kafka"},
		{"cockroachdb/cockroach:v23.1", "cockroach"},
		{"mongo:7", "mongodb"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  dep:
    image: `+c.image+`
`)
		sys, err := detect.Detect(compose)
		if err != nil {
			t.Fatalf("Detect(%s): %v", c.image, err)
		}
		if len(sys.Deps) != 1 {
			t.Fatalf("image %s: got %d deps, want 1: %+v", c.image, len(sys.Deps), sys.Deps)
		}
		if sys.Deps[0].Type != c.want {
			t.Errorf("image %s: type = %q, want %q", c.image, sys.Deps[0].Type, c.want)
		}
	}
}

// spec: R-DET-11
func TestComposeInterpolationIsResolvedByComposeGo(t *testing.T) {
	dir := t.TempDir()
	// Hand-rolled YAML would treat "${DB_IMAGE:-postgres:16}" as a literal
	// string; compose-go resolves the default when the env var is unset.
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: ${DB_IMAGE:-postgres:16}
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(sys.Deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(sys.Deps), sys.Deps)
	}
	if sys.Deps[0].Type != "postgresql" {
		t.Errorf("type = %q, want postgresql (interpolated default not resolved)", sys.Deps[0].Type)
	}
}

// spec: R-DET-6
func TestObservabilityTracesFromJaegerImageGivesCausedConfidence(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
  jaeger:
    image: jaegertracing/all-in-one:1.53
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !sys.Obs.Traces {
		t.Errorf("Obs.Traces = false, want true (jaeger present)")
	}
	if sys.Obs.MaxConfidence != "caused" {
		t.Errorf("MaxConfidence = %q, want caused", sys.Obs.MaxConfidence)
	}
	// jaeger is observability infra, not a dependency the SUT relies on to
	// serve requests, and it is not in the R-DET-9 vocabulary either.
	for _, d := range sys.Deps {
		if d.Name == "jaeger" {
			t.Errorf("jaeger classified as a dependency: %+v", d)
		}
	}
	for _, g := range sys.Gaps {
		if got := g; len(got) > 0 && contains(got, "jaeger") {
			t.Errorf("jaeger produced a gap, want it recognized as observability infra: %q", g)
		}
	}
}

// spec: R-DET-6
func TestObservabilityMetricsOnlyGivesCorrelatedConfidence(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
  prometheus:
    image: prom/prometheus:v2.51.0
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !sys.Obs.Metrics {
		t.Errorf("Obs.Metrics = false, want true (prometheus present)")
	}
	if sys.Obs.Traces {
		t.Errorf("Obs.Traces = true, want false (no tracing backend present)")
	}
	if sys.Obs.MaxConfidence != "correlated" {
		t.Errorf("MaxConfidence = %q, want correlated", sys.Obs.MaxConfidence)
	}
}

// spec: R-DET-6
// A repo with no observability infrastructure at all still reports
// `correlated`, never "" and never "none" (TBD-6): TortureU schedules the
// faults and k6 measures the breach, so single-fault time-window
// attribution holds with no cooperation from the target. An empty value
// would JSON-omit itself and render as a blank field, telling the repos
// that most need to hear about their confidence ceiling nothing at all.
func TestNoObservabilityInfraStillReportsCorrelatedConfidence(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if sys.Obs.Traces || sys.Obs.Metrics || sys.Obs.Logs {
		t.Fatalf("Obs = %+v, want no observability infrastructure detected", sys.Obs)
	}
	if sys.Obs.MaxConfidence != "correlated" {
		t.Errorf("MaxConfidence = %q, want correlated (we schedule the faults — D-4)", sys.Obs.MaxConfidence)
	}
}

// spec: R-DET-4
func TestDependencyAddressUsesDeclaredPortNotDefaultTable(t *testing.T) {
	dir := t.TempDir()
	// redis's well-known port is 6379; this service publishes a container
	// port of 7000. A static well-known-port table would derive the wrong
	// address, which would then poison the egress entry.
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  cache:
    image: redis:7
    ports:
      - "7000:7000"
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(sys.Deps) != 1 {
		t.Fatalf("got %d deps, want 1: %+v", len(sys.Deps), sys.Deps)
	}
	if got, want := sys.Deps[0].Address, "cache:7000"; got != want {
		t.Errorf("address = %q, want %q (declared port, not the 6379 default)", got, want)
	}
}

// spec: R-DET-4
func TestInComposeDependencyEgressIsClassInternal(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	addr := sys.Deps[0].Address
	if got, want := sys.EgressClass[addr], "internal"; got != want {
		t.Errorf("EgressClass[%q] = %q, want %q (in-compose dependency)", addr, got, want)
	}
}

// spec: R-DET-12
// Observability infrastructure is its own classification — neither a
// dependency (the SUT doesn't need it to serve requests) nor a gap (we
// know exactly what it is). This is a verification test: the exclusion
// from Deps/Gaps was already exercised incidentally by the R-DET-6
// observability-confidence tests, but nothing previously cited R-DET-12
// itself or covered more than one recognized backend at a time.
func TestObservabilityInfraIsExcludedFromDepsAndGapsAcrossBackends(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
  db:
    image: postgres:16
  tempo:
    image: tempo:2.4.0
  zipkin:
    image: zipkin:2
  loki:
    image: grafana/loki:2.9.0
  otelcol:
    image: otel/opentelemetry-collector-contrib:0.96.0
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	obsServices := []string{"tempo", "zipkin", "loki", "otelcol"}
	for _, name := range obsServices {
		for _, d := range sys.Deps {
			if d.Name == name {
				t.Errorf("%s classified as a dependency: %+v (R-DET-12: observability infra is its own classification)", name, d)
			}
		}
		for _, g := range sys.Gaps {
			if contains(g, name) {
				t.Errorf("%s produced a gap %q, want it recognized as observability infra (R-DET-12)", name, g)
			}
		}
	}
	// Only the real dependency (postgres) should have made it through.
	if len(sys.Deps) != 1 || sys.Deps[0].Name != "db" {
		t.Errorf("Deps = %+v, want just db — observability services must not leak into the dependency list", sys.Deps)
	}
	if len(sys.Gaps) != 0 {
		t.Errorf("Gaps = %v, want none — every service here is either a known dependency or recognized observability infra", sys.Gaps)
	}
	if !sys.Obs.Traces || !sys.Obs.Logs || !sys.Obs.Metrics {
		t.Errorf("Obs = %+v, want traces/logs/metrics all true (tempo+zipkin, loki, otel-collector)", sys.Obs)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
