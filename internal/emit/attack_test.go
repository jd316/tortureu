package emit

import (
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/detect"
)

// attackFixture is a torture.yaml with a base_url, a bounded load window and
// a couple of faults, used by the slowloris tests. Its own fixture (not the
// shared netemFixture) so concurrent edits to that one cannot shift these
// assertions.
const attackFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://checkout-api:8080
egress:
  default: deny
  hosts:
    kafka:9092: { class: internal }
load:
  engine: k6
  model: arrival_rate
  stages:
    - phase: ramp
      to: 200rps
      over: 30s
    - phase: peak
      hold: 500rps
      for: 60s
faults:
  - name: pg_slow
    at: peak
    for: 30s
    target: kafka:9092
    inject: { latency: 300ms, jitter: 50ms }
assert:
  - http_req_duration: ["p(95)<500"]
`

// R-CLI-8 (proposed): `tortureu emit slowloris` translates torture.yaml
// into a slowhttptest command aimed at the operator's OWN SUT
// (target.base_url) — a defensive slow-request capacity test, not fault
// injection. It targets base_url verbatim (never a guessed host) and states
// plainly that it is a self-test of a system the operator runs.
func TestSlowloris_TargetsOwnSUTBaseURL(t *testing.T) {
	// spec: R-CLI-8
	cfg := mustParse(t, attackFixture)
	out, err := attackSlowloris(cfg)
	if err != nil {
		t.Fatalf("attackSlowloris: %v", err)
	}
	if !strings.Contains(out, "slowhttptest -H -c") {
		t.Errorf("expected a slow-headers (Slowloris) slowhttptest command, got:\n%s", out)
	}
	if !strings.Contains(out, "-u http://checkout-api:8080/") {
		t.Errorf("expected the command to target target.base_url, got:\n%s", out)
	}
	low := strings.ToLower(out)
	if !strings.Contains(low, "own") || !strings.Contains(low, "defensive") {
		t.Errorf("expected the header to state plainly this is a defensive self-test of the operator's own service, got:\n%s", out)
	}
}

// R-CLI-8 (proposed): the test length is sized from load.stages' total
// duration when torture.yaml declares one, rather than an arbitrary
// constant, mirroring how the protocol-load emitters size from load.
func TestSlowloris_SizesDurationFromLoad(t *testing.T) {
	// spec: R-CLI-8
	cfg := mustParse(t, attackFixture)
	out, err := attackSlowloris(cfg)
	if err != nil {
		t.Fatalf("attackSlowloris: %v", err)
	}
	// 30s ramp + 60s hold = 90s total.
	if !strings.Contains(out, "-l 90 ") {
		t.Errorf("expected -l 90 sized from load.stages total (30s+60s), got:\n%s", out)
	}
}

// R-CLI-8 (proposed): with no base_url there is no SUT URL to test, and it
// MUST NOT be guessed — the tool refuses with an explanatory comment rather
// than emitting a command against a made-up host.
func TestSlowloris_RefusesWithoutBaseURL(t *testing.T) {
	// spec: R-CLI-8
	cfg := &config.Config{Target: config.Target{Service: "svc"}}
	out, err := attackSlowloris(cfg)
	if err != nil {
		t.Fatalf("attackSlowloris: %v", err)
	}
	if strings.Contains(out, "slowhttptest -") {
		t.Errorf("expected no slowhttptest command without a base_url, got:\n%s", out)
	}
	if !strings.Contains(out, "base_url") {
		t.Errorf("expected the refusal to name the missing base_url, got:\n%s", out)
	}
}

// R-CLI-8 (proposed): a load-shaped tool emits request traffic only, so
// every fault in torture.yaml is reported as not translated rather than
// silently dropped.
func TestSlowloris_ReportsUntranslatedFaults(t *testing.T) {
	// spec: R-CLI-8
	cfg := mustParse(t, attackFixture)
	out, err := attackSlowloris(cfg)
	if err != nil {
		t.Fatalf("attackSlowloris: %v", err)
	}
	if !strings.Contains(out, `fault "pg_slow"`) {
		t.Errorf("expected pg_slow to be reported as not translated, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------
// rebalance
// ---------------------------------------------------------------------

func attackKafkaSystem() *detect.System {
	return &detect.System{
		SUT:  "checkout-api",
		Lang: "go",
		Deps: []detect.Dep{{Name: "kafka", Type: "kafka", Address: "kafka:9092"}},
	}
}

// R-CLI-8 (proposed): `tortureu emit rebalance` reaches the detected kafka
// broker via "--network container:<broker>" (the sysbench/memtier
// convention) and drives a rebalance storm by repeatedly joining a consumer
// group with a transient consumer killed mid-batch.
func TestRebalance_UsesDetectedKafkaBroker(t *testing.T) {
	// spec: R-CLI-8
	cfg := mustParse(t, attackFixture)
	out, err := attackRebalance(cfg, attackKafkaSystem())
	if err != nil {
		t.Fatalf("attackRebalance: %v", err)
	}
	if !strings.Contains(out, "--network container:kafka") {
		t.Errorf("expected the detected kafka broker to be reached via its container netns, got:\n%s", out)
	}
	if !strings.Contains(out, "topic consume") || !strings.Contains(out, "timeout") {
		t.Errorf("expected a timeout-bounded transient consumer join, got:\n%s", out)
	}
}

// R-CLI-8 (proposed): the consumer group and topic are not in torture.yaml's
// schema and MUST NOT be guessed — they are emitted as CHANGE_ME
// placeholders the operator must fill in.
func TestRebalance_DoesNotGuessGroupOrTopic(t *testing.T) {
	// spec: R-CLI-8
	cfg := mustParse(t, attackFixture)
	out, err := attackRebalance(cfg, attackKafkaSystem())
	if err != nil {
		t.Fatalf("attackRebalance: %v", err)
	}
	if strings.Count(out, "CHANGE_ME") < 2 {
		t.Errorf("expected CHANGE_ME placeholders for both group and topic, got:\n%s", out)
	}
}

// R-CLI-8 / R-COV-6: with no kafka dependency detected there is nothing to
// address, and the tool says so rather than guessing a broker.
func TestRebalance_RefusesWithoutKafka(t *testing.T) {
	// spec: R-CLI-8
	cfg := mustParse(t, attackFixture)
	out, err := attackRebalance(cfg, &detect.System{})
	if err != nil {
		t.Fatalf("attackRebalance: %v", err)
	}
	if strings.Contains(out, "topic consume") {
		t.Errorf("expected no command without a detected kafka broker, got:\n%s", out)
	}
	if !strings.Contains(out, "kafka") {
		t.Errorf("expected the refusal to name kafka, got:\n%s", out)
	}
}

// ---------------------------------------------------------------------
// dlq-test
// ---------------------------------------------------------------------

// R-CLI-8 (proposed): against a detected kafka broker, `tortureu emit
// dlq-test` floods the dead-letter topic to overflow its retention.
func TestDLQ_KafkaFloodsDLQTopic(t *testing.T) {
	// spec: R-CLI-8
	cfg := mustParse(t, attackFixture)
	out, err := attackDLQ(cfg, attackKafkaSystem())
	if err != nil {
		t.Fatalf("attackDLQ: %v", err)
	}
	if !strings.Contains(out, "topic produce") || !strings.Contains(out, "retention.bytes") {
		t.Errorf("expected a flood-produce plus a retention bound to overflow, got:\n%s", out)
	}
	if !strings.Contains(out, "--network container:kafka") {
		t.Errorf("expected the detected kafka broker to be reached via its container netns, got:\n%s", out)
	}
	if !strings.Contains(out, "CHANGE_ME") {
		t.Errorf("expected the DLQ topic name to be a CHANGE_ME placeholder, got:\n%s", out)
	}
}

// R-CLI-8 (proposed): with only an sqs dependency, the emit floods the DLQ
// queue via the aws CLI and refuses to guess the queue URL.
func TestDLQ_SQSFloodsQueue(t *testing.T) {
	// spec: R-CLI-8
	cfg := mustParse(t, attackFixture)
	sys := &detect.System{Deps: []detect.Dep{{Name: "sqs", Type: "sqs"}}}
	out, err := attackDLQ(cfg, sys)
	if err != nil {
		t.Fatalf("attackDLQ: %v", err)
	}
	if !strings.Contains(out, "aws sqs send-message") {
		t.Errorf("expected an aws sqs send-message flood, got:\n%s", out)
	}
	if !strings.Contains(out, "CHANGE_ME") {
		t.Errorf("expected the queue URL to be a CHANGE_ME placeholder, got:\n%s", out)
	}
}

// R-CLI-8 / R-COV-6: with neither kafka nor sqs detected, the tool refuses
// and names both required dependency types.
func TestDLQ_RefusesWithoutKafkaOrSQS(t *testing.T) {
	// spec: R-CLI-8
	cfg := mustParse(t, attackFixture)
	out, err := attackDLQ(cfg, &detect.System{})
	if err != nil {
		t.Fatalf("attackDLQ: %v", err)
	}
	if strings.Contains(out, "topic produce") || strings.Contains(out, "aws sqs") {
		t.Errorf("expected no command without kafka or sqs, got:\n%s", out)
	}
	if !strings.Contains(out, "kafka") || !strings.Contains(out, "sqs") {
		t.Errorf("expected the refusal to name both kafka and sqs, got:\n%s", out)
	}
}

// R-CLI-8: all three are reachable through the shared registry dispatch.
func TestAttack_Registered(t *testing.T) {
	// spec: R-CLI-8
	for _, name := range []string{"slowloris", "rebalance", "dlq-test"} {
		if _, ok := registry[name]; !ok {
			t.Errorf("emitter %q was not registered", name)
		}
	}
	if NeedsSystem("slowloris") {
		t.Error("slowloris must not need *detect.System")
	}
	if !NeedsSystem("rebalance") || !NeedsSystem("dlq-test") {
		t.Error("rebalance and dlq-test must need *detect.System")
	}
}
