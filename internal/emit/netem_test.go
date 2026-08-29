package emit

import (
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/config"
)

const netemFixture = `
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
  - name: pg_slow
    at: peak
    for: 30s
    target: postgres:5432
    inject: { latency: 300ms, jitter: 50ms }
  - name: stripe_slow
    at: peak
    for: 30s
    target: api.stripe.com
    inject: { bandwidth: 1mbps }
  - name: pg_dies
    at: peak
    for: 10s
    target: postgres:5432
    inject: { down: true }
  - name: cpu_squeeze
    at: peak
    for: 10s
    target: checkout-api
    inject: { cpu: 90%, workers: 4 }
assert:
  - http_req_duration: ["p(95)<500"]
`

func mustParse(t *testing.T, raw string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("config.Parse: %v", err)
	}
	return cfg
}

// R-CLI-8 (proposed): `tortureu emit netem` translates latency and
// bandwidth faults into raw `tc qdisc ... netem` commands, run inside the
// affected container's network namespace via `docker exec`. It only
// touches the container's whole interface — it cannot scope to one
// destination the way pumba's --target flag can — so it names the
// container it should apply to per resolveContainer's rules, and skips
// (with a comment, not silently) any fault verb it does not translate.
func TestNetem_LatencyOnInternalDependency_UsesDependencyContainer(t *testing.T) {
	cfg := mustParse(t, netemFixture)
	out, err := Netem(cfg)
	if err != nil {
		t.Fatalf("Netem: %v", err)
	}
	if !strings.Contains(out, "docker exec postgres tc qdisc add dev eth0 root netem delay 300ms 50ms") {
		t.Errorf("expected a delay command against the postgres container, got:\n%s", out)
	}
}

func TestNetem_BandwidthOnExternalHost_UsesSUTContainer(t *testing.T) {
	cfg := mustParse(t, netemFixture)
	out, err := Netem(cfg)
	if err != nil {
		t.Fatalf("Netem: %v", err)
	}
	// 1mbps -> 1,000,000 bits/s -> 125 KB/s (decimal) -> 1000 kbit/s.
	if !strings.Contains(out, "docker exec checkout-api tc qdisc add dev eth0 root netem rate 1000kbit") {
		t.Errorf("expected a rate command against the SUT container, got:\n%s", out)
	}
}

func TestNetem_CleansUpAfterForDuration(t *testing.T) {
	cfg := mustParse(t, netemFixture)
	out, err := Netem(cfg)
	if err != nil {
		t.Fatalf("Netem: %v", err)
	}
	if !strings.Contains(out, "sleep 30s && docker exec postgres tc qdisc del dev eth0 root") {
		t.Errorf("expected a matching teardown after the fault's for: duration, got:\n%s", out)
	}
}

func TestNetem_SkipsVerbsItDoesNotTranslate(t *testing.T) {
	cfg := mustParse(t, netemFixture)
	out, err := Netem(cfg)
	if err != nil {
		t.Fatalf("Netem: %v", err)
	}
	if !strings.Contains(out, `fault "pg_dies" (inject: down): not translated by netem`) {
		t.Errorf("expected pg_dies to be reported as skipped, got:\n%s", out)
	}
	if !strings.Contains(out, `fault "cpu_squeeze" (inject: cpu): not translated by netem`) {
		t.Errorf("expected cpu_squeeze to be reported as skipped, got:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "pg_dies") && strings.Contains(line, "tc qdisc add") {
			t.Errorf("pg_dies must not produce a netem add command, got line: %s", line)
		}
	}
}

func TestNetem_UnknownFaultStillErrorsOnMalformedInput(t *testing.T) {
	// A Fault built without going through config.Parse (e.g. hand-edited)
	// with an unrecognized verb must still surface as an error, not a
	// silent no-op — Netem delegates verb validation to fault.Translate.
	cfg := &config.Config{
		Target: config.Target{Service: "svc"},
		Faults: []config.Fault{{Name: "bad", Target: "svc", Verb: "not_a_verb", Inject: map[string]any{"not_a_verb": true}}},
	}
	if _, err := Netem(cfg); err == nil {
		t.Fatal("expected an error for an unrecognized verb")
	}
}
