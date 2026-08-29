package emit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/config"
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

// spec: R-CFG-6 — the LoadTestShape survives, but its job is inverted: it
// holds exactly one user (the arrival pacer) for the run's duration, and
// deliberately does NOT use user_count as the load knob, because
// user_count is the size of a closed pool.
func TestLocust_LoadShapeHoldsASinglePacerNotAUserPool(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Locust(cfg)
	if err != nil {
		t.Fatalf("Locust: %v", err)
	}
	if !strings.Contains(out, "class TortureULoadShape(LoadTestShape)") {
		t.Errorf("expected a custom LoadTestShape, got:\n%s", out)
	}
	if !strings.Contains(out, "return (1, 1)") {
		t.Errorf("expected the shape to hold exactly one pacer user, got:\n%s", out)
	}
	if !strings.Contains(out, "TOTAL_RUNTIME") {
		t.Errorf("expected the shape to end the run at the last stage's end, got:\n%s", out)
	}
}

// spec: R-CFG-6 — the single pacer user still targets target.base_url,
// and there is deliberately no longer a @task per scenario: in an open
// model the pacer picks each arrival's flow (see
// TestLocust_ScenarioTableCarriesWeightsAndFlows), so Locust's own task
// scheduler must not be the thing deciding the mix.
func TestLocust_HostIsSetAndSchedulingIsNotLeftToTaskWeights(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Locust(cfg)
	if err != nil {
		t.Fatalf("Locust: %v", err)
	}
	if !strings.Contains(out, `host = "http://localhost:8080"`) {
		t.Errorf("expected host set from target.base_url, got:\n%s", out)
	}
	if strings.Contains(out, "@task(3)") || strings.Contains(out, "@task(1)") {
		t.Errorf("expected no per-scenario @task weights in an open model, got:\n%s", out)
	}
	if !strings.Contains(out, "self.client.request(method, path, data=body)") {
		t.Errorf("expected each arrival to drive its flow through one request call, got:\n%s", out)
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

// spec: R-CFG-6 — load.model MUST be arrival_rate (an OPEN model): the
// arrival rate is independent of response time, precisely so a slowdown
// does not silently throttle the offered load (coordinated omission).
//
// Locust ships no open-model executor, and its two candidate wait-time
// primitives are both "at most" pacers on a fixed pool of looping users,
// so neither can satisfy R-CFG-6. Measured on this host (locust 2.46.3,
// 200rps declared for 20s against a Go target with fixed injected
// latency, arrivals counted server-side):
//
//	wait_time                latency  achieved
//	constant_pacing(1)          10ms   200 rps
//	constant_pacing(1)         500ms   200 rps
//	constant_pacing(1)         900ms   200 rps
//	constant_pacing(1)        1200ms   170 rps
//	constant_pacing(1)        2000ms   100 rps  (arrivals bunched 200,0,200,0)
//	constant_throughput(1)     500ms   200 rps
//	constant_throughput(1)    2000ms   100 rps
//
// i.e. achieved = declared * min(1, pacing_interval / response_time) —
// the rate holds only while responses stay under the pacing interval,
// which is exactly the regime where the open model does not matter.
//
// The emit therefore must NOT drive the rate from a wait_time at all: it
// paces arrivals itself and dispatches each into its own greenlet, so an
// in-flight request cannot delay the next arrival.
func TestLocust_EmitsOpenModelArrivalPacer(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Locust(cfg)
	if err != nil {
		t.Fatalf("Locust: %v", err)
	}
	if !strings.Contains(out, "gevent.spawn_later") {
		t.Errorf("expected arrivals dispatched via gevent so response time cannot gate them, got:\n%s", out)
	}
	if strings.Contains(out, "wait_time = constant_pacing(") || strings.Contains(out, "wait_time = constant_throughput(") {
		t.Errorf("expected the arrival rate NOT to be driven by a closed-model wait_time, got:\n%s", out)
	}
	if !strings.Contains(out, "def rate_at(") || !strings.Contains(out, "STAGES = [") {
		t.Errorf("expected a declared arrival-rate schedule, got:\n%s", out)
	}
}

// spec: R-CFG-6 — the stage table the pacer reads must carry the declared
// load.stages rates and durations, with a ramp's implicit "from" being the
// previous stage's end rate.
func TestLocust_StageTableCarriesDeclaredArrivalRates(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Locust(cfg)
	if err != nil {
		t.Fatalf("Locust: %v", err)
	}
	if !strings.Contains(out, `(0, 30, 0, 200),  # phase "ramp_up"`) {
		t.Errorf("expected the ramp_up stage 0->200rps over 30s, got:\n%s", out)
	}
	if !strings.Contains(out, `(30, 90, 200, 200),  # phase "peak"`) {
		t.Errorf("expected the peak stage held at 200rps for 60s, got:\n%s", out)
	}
}

// spec: R-CFG-6 — scenario weighting must survive the move off @task(n):
// in an open model the pacer picks the flow per arrival, so the weights
// live in a table it samples rather than in Locust's task scheduler.
func TestLocust_ScenarioTableCarriesWeightsAndFlows(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Locust(cfg)
	if err != nil {
		t.Fatalf("Locust: %v", err)
	}
	if !strings.Contains(out, `(3, [("GET", "/products", None)]),  # scenario "browse"`) {
		t.Errorf("expected the browse scenario weighted 3, got:\n%s", out)
	}
	if !strings.Contains(out, `("POST", "/cart", "{\"sku\":\"abc\"}")`) {
		t.Errorf("expected the checkout POST with its body, got:\n%s", out)
	}
	if !strings.Contains(out, `("POST", "/checkout", None)`) {
		t.Errorf("expected the checkout second step, got:\n%s", out)
	}
}

// spec: R-CFG-6 — the header must state what was actually measured, not a
// general disclaimer. It must also keep the one limitation that genuinely
// survives: the pacer is a single Locust user, so `--worker` distributed
// mode does not divide the rate across workers.
func TestLocust_HeaderStatesMeasuredEvidenceAndSurvivingLimit(t *testing.T) {
	cfg := mustParse(t, loadgenFixture)
	out, err := Locust(cfg)
	if err != nil {
		t.Fatalf("Locust: %v", err)
	}
	for _, want := range []string{"OPEN MODEL", "2.46.3", "2000ms", "Distributed mode"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected the header to mention %q, got:\n%s", want, out)
		}
	}
}
