package emit

import (
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/config"
)

// R-CLI-8 (proposed): `tortureu emit pumba` translates network faults
// (latency, bandwidth) via `pumba netem`, scoped to one destination with
// pumba's own --target flag when the fault names an external dependency,
// and container-level faults (kill, graceful, pause, cpu) via pumba's
// kill/pause/stress subcommands. Faults this tool does not vouch for
// (down, mem/io/fd stress, error_rate/poison_pill/duplicate, slicer,
// timeout, reset_peer) are reported as skipped, never guessed.
const pumbaFixture = `
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
    - phase: peak
      hold: 500rps
      for: 60s
faults:
  - name: stripe_slow
    at: peak
    for: 40s
    target: api.stripe.com
    inject: { latency: 2s }
  - name: cpu_squeeze
    at: peak
    for: 20s
    target: checkout-api
    inject: { cpu: 90%, workers: 4 }
  - name: pod_pause
    at: peak
    for: 5s
    target: checkout-api
    inject: { pause: true }
  - name: pod_kill
    at: peak
    target: checkout-api
    inject: { kill: true }
  - name: mem_squeeze
    at: peak
    for: 10s
    target: checkout-api
    inject: { mem: 512, workers: 2 }
assert:
  - http_req_duration: ["p(95)<500"]
`

func TestPumba_LatencyOnExternalHost_ScopesWithTargetFlag(t *testing.T) {
	cfg := mustParse(t, pumbaFixture)
	out, err := Pumba(cfg)
	if err != nil {
		t.Fatalf("Pumba: %v", err)
	}
	if !strings.Contains(out, "pumba netem --duration 40s delay --time 2000 --target api.stripe.com checkout-api") {
		t.Errorf("expected a scoped netem delay command, got:\n%s", out)
	}
}

func TestPumba_CPU_UsesStressSubcommand(t *testing.T) {
	cfg := mustParse(t, pumbaFixture)
	out, err := Pumba(cfg)
	if err != nil {
		t.Fatalf("Pumba: %v", err)
	}
	if !strings.Contains(out, `pumba stress --duration 20s --stress-image alexeiled/stress-ng --stressors "--cpu 4 --cpu-load 90" checkout-api`) {
		t.Errorf("expected a stress-ng cpu command, got:\n%s", out)
	}
}

func TestPumba_PauseAndKill_UseNativeSubcommands(t *testing.T) {
	cfg := mustParse(t, pumbaFixture)
	out, err := Pumba(cfg)
	if err != nil {
		t.Fatalf("Pumba: %v", err)
	}
	if !strings.Contains(out, "pumba pause --duration 5s checkout-api") {
		t.Errorf("expected a pause command, got:\n%s", out)
	}
	if !strings.Contains(out, "pumba kill --signal SIGKILL checkout-api") {
		t.Errorf("expected a kill command, got:\n%s", out)
	}
}

func TestPumba_SkipsMemIOFDStress(t *testing.T) {
	cfg := mustParse(t, pumbaFixture)
	out, err := Pumba(cfg)
	if err != nil {
		t.Fatalf("Pumba: %v", err)
	}
	if !strings.Contains(out, `fault "mem_squeeze" (inject: mem): not translated by pumba`) {
		t.Errorf("expected mem_squeeze to be reported as skipped, got:\n%s", out)
	}
}

func TestPumba_UnknownVerbErrors(t *testing.T) {
	cfg := &config.Config{
		Target: config.Target{Service: "svc"},
		Faults: []config.Fault{{Name: "bad", Target: "svc", Verb: "not_a_verb", Inject: map[string]any{"not_a_verb": true}}},
	}
	if _, err := Pumba(cfg); err == nil {
		t.Fatal("expected an error for an unrecognized verb")
	}
}
