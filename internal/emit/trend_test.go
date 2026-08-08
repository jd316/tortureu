package emit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// spec: R-CLI-8

const bencherFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://localhost:8080
egress:
  default: deny
  hosts:
    postgres:5432: { class: internal }
load:
  engine: k6
  model: arrival_rate
  stages:
    - phase: ramp_up
      to: 500rps
      over: 60s
    - phase: peak
      hold: 500rps
      for: 180s
faults:
  - name: pg_slow
    at: peak
    for: 60s
    target: postgres:5432
    inject: { latency: 300ms, jitter: 50ms }
assert:
  - http_req_duration: ["p(95)<500", "p(99)<1500"]
  - http_reqs: ["rate>100"]
  - http_req_failed: ["rate<0.01"]
  - promql: 'sum(rate(app_retries_total[30s])) < 100'
  - sql: 'select count(*) from orders where total is null'
`

// spec: R-CLI-8 — bencher is registry.yaml `when: always`, so it emits from
// torture.yaml alone and must never need a *detect.System.
func TestBencher_EmitsWithoutDetection(t *testing.T) {
	out, err := Bencher(mustParse(t, bencherFixture), nil)
	if err != nil {
		t.Fatalf("Bencher: %v", err)
	}
	if !strings.Contains(out, "bencher run") || !strings.Contains(out, "--adapter json") {
		t.Errorf("expected a `bencher run --adapter json` invocation, got:\n%s", out)
	}
	if !strings.Contains(out, "--file") {
		t.Errorf("expected the BMF document to be handed over with --file, got:\n%s", out)
	}
	if NeedsSystem("bencher") {
		t.Error("bencher must not be registered as needing detection: registry.yaml says when: always")
	}
}

// spec: R-CLI-8 — the only measure names emitted are Bencher's own built-in
// ones. A measure key Bencher does not know is auto-created server-side with
// the fallback unit "Measure (units)", which would label a millisecond a
// unitless number forever, so nothing else may appear.
func TestBencher_UsesOnlyBuiltInMeasures(t *testing.T) {
	out, err := Bencher(mustParse(t, bencherFixture), nil)
	if err != nil {
		t.Fatalf("Bencher: %v", err)
	}
	// Every measure this emits is the second argument of a generated
	// entry(...) call in the jq program; nothing else can become a measure.
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "entry(") {
			continue
		}
		parts := strings.Split(trimmed, "; ")
		if len(parts) < 2 {
			t.Fatalf("unparseable generated entry() call: %q", trimmed)
		}
		if measure := parts[1]; measure != `"latency"` && measure != `"throughput"` {
			t.Errorf("emitted a measure Bencher has no built-in for: %s (in %q)", measure, trimmed)
		}
	}
	if !strings.Contains(out, `"latency"`) {
		t.Errorf("expected the built-in latency measure for http_req_duration, got:\n%s", out)
	}
	if !strings.Contains(out, `"throughput"`) {
		t.Errorf("expected the built-in throughput measure for http_reqs, got:\n%s", out)
	}
}

// spec: R-CLI-8 — only metrics torture.yaml actually asserts are tracked.
// A trend line for a metric nobody asserted is a number with no owner.
func TestBencher_TracksOnlyAssertedMetrics(t *testing.T) {
	out, err := Bencher(mustParse(t, bencherFixture), nil)
	if err != nil {
		t.Fatalf("Bencher: %v", err)
	}
	if strings.Contains(out, "iteration_duration") {
		t.Errorf("iteration_duration is not asserted in the fixture but was tracked:\n%s", out)
	}
	if !strings.Contains(out, "http_req_duration") {
		t.Errorf("asserted metric http_req_duration missing:\n%s", out)
	}
}

// spec: R-CLI-8 — anything not translated is reported, never dropped: the
// non-duration k6 assert, the promql: assert and the sql: assert each have
// to be named with a reason, and so does every fault.
func TestBencher_ReportsEverythingItDoesNotTranslate(t *testing.T) {
	out, err := Bencher(mustParse(t, bencherFixture), nil)
	if err != nil {
		t.Fatalf("Bencher: %v", err)
	}
	for _, want := range []string{"http_req_failed", "promql:", "sql:", "pg_slow"} {
		if !strings.Contains(out, want) {
			t.Errorf("untranslated %q not reported in the output:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "not translated") {
		t.Errorf("expected an explicit \"not translated\" report:\n%s", out)
	}
}

// spec: R-CLI-8 — no project slug, branch, testbed or threshold boundary is
// invented. A guessed project slug uploads one repo's numbers into another
// project's trend, which cannot be taken back.
func TestBencher_InventsNoProjectOrThreshold(t *testing.T) {
	out, err := Bencher(mustParse(t, bencherFixture), nil)
	if err != nil {
		t.Fatalf("Bencher: %v", err)
	}
	if !strings.Contains(out, "BENCHER_PROJECT") {
		t.Errorf("expected the project to come from BENCHER_PROJECT, got:\n%s", out)
	}
	if strings.Contains(out, "--project checkout") || strings.Contains(out, "--project tortureu") {
		t.Errorf("emitted a guessed --project value:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // commented-out example flags are documentation, not a choice
		}
		if strings.Contains(trimmed, "--threshold-upper-boundary") || strings.Contains(trimmed, "--threshold-test") {
			t.Errorf("emitted an active threshold policy we have no basis to choose: %q", line)
		}
	}
}

// spec: R-VER-2 — a verdict with status error/aborted carries no measurement
// of the system under test, so feeding it to a trend would record TortureU's
// own failure as a performance datapoint.
func TestBencher_RefusesErroredVerdicts(t *testing.T) {
	out, err := Bencher(mustParse(t, bencherFixture), nil)
	if err != nil {
		t.Fatalf("Bencher: %v", err)
	}
	if !strings.Contains(out, "error") || !strings.Contains(out, "aborted") {
		t.Errorf("expected the script to refuse status:error/aborted verdicts:\n%s", out)
	}
}

// spec: R-CLI-8 — the emitted jq program must be a program jq accepts. This
// runs the real jq binary over the real emitted script against a synthetic
// verdict shaped like VERDICT.md §1, and checks the BMF that comes out is
// valid Bencher Metric Format (benchmark -> measure -> {value}).
func TestBencher_JqProgramProducesValidBMF(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	out, err := Bencher(mustParse(t, bencherFixture), nil)
	if err != nil {
		t.Fatalf("Bencher: %v", err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "trend.sh")
	if err := os.WriteFile(script, []byte(out), 0o755); err != nil {
		t.Fatal(err)
	}
	verdictPath := filepath.Join(dir, "verdict.json")
	if err := os.WriteFile(verdictPath, []byte(bencherVerdictJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	bmf := filepath.Join(dir, "bmf.json")

	cmd := exec.Command("bash", script, verdictPath)
	cmd.Env = append(os.Environ(),
		"BENCHER_PROJECT=tortureu-selftest",
		"TORTUREU_BMF_OUT="+bmf,
		"TORTUREU_BENCHER_BMF_ONLY=1",
	)
	if combined, rerr := cmd.CombinedOutput(); rerr != nil {
		t.Fatalf("running the emitted script: %v\n%s", rerr, combined)
	}
	raw, err := os.ReadFile(bmf)
	if err != nil {
		t.Fatalf("the script did not write a BMF document: %v", err)
	}
	got := string(raw)
	// p(95) = 412.5ms must arrive as nanoseconds, Bencher's unit for latency.
	if !strings.Contains(got, "412500000") {
		t.Errorf("expected p(95) 412.5ms converted to 412500000ns, got:\n%s", got)
	}
	if !strings.Contains(got, `"throughput"`) || !strings.Contains(got, "487") {
		t.Errorf("expected http_reqs rate carried as throughput, got:\n%s", got)
	}
	if strings.Contains(got, "http_req_failed") {
		t.Errorf("http_req_failed is untranslatable to a built-in measure but appeared in the BMF:\n%s", got)
	}
}

// bencherVerdictJSON is a verdict document in VERDICT.md §1's shape, with a
// metrics map in the form internal/k6's IngestSummary produces.
const bencherVerdictJSON = `{
  "run_id": "2026-08-09T00:00:00Z-checkout-spike-a3f",
  "scenario": "checkout-spike",
  "status": "fail",
  "commit": "a3f19c2",
  "findings": [],
  "passed": [],
  "egress_audit": {"mocked": [], "blocked": [], "real": [], "unclassified": []},
  "observability": {"traces": false, "metrics": true, "logs": true},
  "metrics": {
    "http_req_duration": {"avg": 180.25, "min": 12, "med": 150, "max": 4218, "p(95)": 412.5, "p(99)": 1490},
    "http_reqs": {"count": 91000, "rate": 487.3},
    "http_req_failed": {"value": 0.003}
  }
}`

// spec: R-CLI-8 — the end-to-end claim in this file's VERIFICATION STATUS
// block: the emitted script, run verbatim, is accepted by a real bencher
// binary. Off by default (the gate must not need a CLI install):
//
//	curl -sSfL https://bencher.dev/download/install-cli.sh | sh
//	TORTUREU_EMIT_LIVE=1 go test ./internal/emit/ -run LiveBencher -v
func TestBencher_AcceptedByRealBencherCLI(t *testing.T) {
	if os.Getenv("TORTUREU_EMIT_LIVE") != "1" {
		t.Skip("set TORTUREU_EMIT_LIVE=1 (and install the bencher CLI) to verify against the real binary")
	}
	if _, err := exec.LookPath("bencher"); err != nil {
		t.Skip("bencher not on PATH")
	}
	out, err := Bencher(mustParse(t, bencherFixture), nil)
	if err != nil {
		t.Fatalf("Bencher: %v", err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "trend.sh")
	if err := os.WriteFile(script, []byte(out), 0o755); err != nil {
		t.Fatal(err)
	}
	verdictPath := filepath.Join(dir, "verdict.json")
	if err := os.WriteFile(verdictPath, []byte(bencherVerdictJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, verdictPath)
	cmd.Env = append(os.Environ(),
		"BENCHER_PROJECT=tortureu-selftest",
		"TORTUREU_BMF_OUT="+filepath.Join(dir, "bmf.json"),
		"TORTUREU_BENCHER_DRY_RUN=1",
	)
	combined, rerr := cmd.CombinedOutput()
	if rerr != nil {
		t.Fatalf("real bencher rejected the emitted run: %v\n%s", rerr, combined)
	}
	t.Logf("bencher accepted the emitted BMF:\n%s", combined)
}
