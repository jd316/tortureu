package emit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/config"
)

// assertPyCompiles shells out to `python3 -m py_compile` (present on this
// machine per pre-flight check) to verify the emitted locustfile is at
// least syntactically valid Python, since Locust itself was only installed
// in a throwaway venv for one-off manual verification, not as a build
// dependency of this repo/test suite.
func assertPyCompiles(t *testing.T, src string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH; skipping syntax check")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "locustfile.py")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write locustfile: %v", err)
	}
	cmd := exec.Command("python3", "-m", "py_compile", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("emitted locustfile is not valid Python: %v\n%s", err, out)
	}
}

// spec: R-CLI-8 — `emit <tool>` generates a runnable command/config for a
// delegate-tier tool from torture.yaml, reusing internal/config's parsed
// Load block (not re-deriving stage/scenario semantics), and reports any
// fault verb it does not translate rather than dropping it. Gatling and
// Locust are not wired into Emit/Tools (internal/emit/emit.go) here — that
// dispatch table is shared with other agents extending this package
// concurrently, so this file only adds Gatling(cfg) and Locust(cfg) as
// directly callable functions per the task's collision-avoidance rule.
const loadgenFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://localhost:8080
egress:
  default: deny
  hosts:
    postgres:5432: { class: internal }
    api.stripe.com: { class: mock, from: spec }
load:
  engine: k6
  model: arrival_rate
  stages:
    - phase: ramp_up
      to: 200rps
      over: 30s
    - phase: peak
      hold: 200rps
      for: 60s
  scenarios:
    - name: browse
      weight: 3
      flow:
        - method: GET
          path: /products
    - name: checkout
      weight: 1
      flow:
        - method: POST
          path: /cart
          body: '{"sku":"abc"}'
        - method: POST
          path: /checkout
faults:
  - name: stripe_slow
    at: peak
    for: 40s
    target: api.stripe.com
    inject: { latency: 2s }
assert:
  - http_req_duration: ["p(95)<500"]
`

func TestGatling_RampStageUsesOpenModelInjection(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Gatling(cfg)
	if err != nil {
		t.Fatalf("Gatling: %v", err)
	}
	if !strings.Contains(out, "rampUsersPerSec(0) to 200 during (30 seconds)") {
		t.Errorf("expected an open-model ramp injection, got:\n%s", out)
	}
}

func TestGatling_HoldStageUsesConstantUsersPerSec(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Gatling(cfg)
	if err != nil {
		t.Fatalf("Gatling: %v", err)
	}
	if !strings.Contains(out, "constantUsersPerSec(200) during (60 seconds)") {
		t.Errorf("expected an open-model hold injection, got:\n%s", out)
	}
}

func TestGatling_WeightedScenariosViaRandomSwitch(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Gatling(cfg)
	if err != nil {
		t.Fatalf("Gatling: %v", err)
	}
	if !strings.Contains(out, `.get("/products")`) {
		t.Errorf("expected the browse scenario's GET, got:\n%s", out)
	}
	if !strings.Contains(out, `.post("/cart")`) || !strings.Contains(out, `.post("/checkout")`) {
		t.Errorf("expected the checkout scenario's POSTs, got:\n%s", out)
	}
	if !strings.Contains(out, "randomSwitch") {
		t.Errorf("expected scenarios combined via randomSwitch to preserve one open arrival stream, got:\n%s", out)
	}
	if !strings.Contains(out, "75.0") || !strings.Contains(out, "25.0") {
		t.Errorf("expected weights 3:1 normalized to 75.0/25.0 percent, got:\n%s", out)
	}
}

func TestGatling_ReportsUntranslatedFault(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Gatling(cfg)
	if err != nil {
		t.Fatalf("Gatling: %v", err)
	}
	if !strings.Contains(out, `fault "stripe_slow" (inject: latency): not translated by gatling`) {
		t.Errorf("expected the fault to be reported as not translated, got:\n%s", out)
	}
}

func TestGatling_UnknownFaultVerbErrors(t *testing.T) {
	cfg := &config.Config{
		Target: config.Target{Service: "svc"},
		Faults: []config.Fault{{Name: "bad", Target: "svc", Verb: "not_a_verb", Inject: map[string]any{"not_a_verb": true}}},
	}
	if _, err := Gatling(cfg); err == nil {
		t.Fatal("expected an error for an unrecognized verb")
	}
}

func TestGatling_HeaderDisclosesVerificationStatus(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Gatling(cfg)
	if err != nil {
		t.Fatalf("Gatling: %v", err)
	}
	if !strings.Contains(out, "compiled and executed") || !strings.Contains(out, "Gatling 3.10.5") {
		t.Errorf("expected the header to disclose gatling verification status, got:\n%s", out)
	}
}

func TestLocust_ProducesLoadShapeApproximatingOpenModel(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Locust(cfg)
	if err != nil {
		t.Fatalf("Locust: %v", err)
	}
	if !strings.Contains(out, "class TortureULoadShape(LoadTestShape)") {
		t.Errorf("expected a custom LoadTestShape, got:\n%s", out)
	}
	if !strings.Contains(out, "200") {
		t.Errorf("expected the 200rps target to appear, got:\n%s", out)
	}
	if !strings.Contains(out, "closed") {
		t.Errorf("expected the header to disclose Locust's closed-model limitation, got:\n%s", out)
	}
}

func TestLocust_WeightedTasksMatchScenarioWeights(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Locust(cfg)
	if err != nil {
		t.Fatalf("Locust: %v", err)
	}
	if !strings.Contains(out, "@task(3)") || !strings.Contains(out, "@task(1)") {
		t.Errorf("expected task weights 3 and 1 from the scenarios, got:\n%s", out)
	}
	if !strings.Contains(out, `self.client.get("/products")`) {
		t.Errorf("expected the browse scenario's GET, got:\n%s", out)
	}
	if !strings.Contains(out, `self.client.post("/cart"`) || !strings.Contains(out, `self.client.post("/checkout"`) {
		t.Errorf("expected the checkout scenario's POSTs, got:\n%s", out)
	}
	if !strings.Contains(out, `host = "http://localhost:8080"`) {
		t.Errorf("expected host set from target.base_url, got:\n%s", out)
	}
}

func TestLocust_ReportsUntranslatedFault(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Locust(cfg)
	if err != nil {
		t.Fatalf("Locust: %v", err)
	}
	if !strings.Contains(out, `fault "stripe_slow" (inject: latency): not translated by locust`) {
		t.Errorf("expected the fault to be reported as not translated, got:\n%s", out)
	}
}

func TestLocust_UnknownFaultVerbErrors(t *testing.T) {
	cfg := &config.Config{
		Target: config.Target{Service: "svc"},
		Faults: []config.Fault{{Name: "bad", Target: "svc", Verb: "not_a_verb", Inject: map[string]any{"not_a_verb": true}}},
	}
	if _, err := Locust(cfg); err == nil {
		t.Fatal("expected an error for an unrecognized verb")
	}
}

func TestLocust_OutputIsValidPython(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Locust(cfg)
	if err != nil {
		t.Fatalf("Locust: %v", err)
	}
	assertPyCompiles(t, out)
}
