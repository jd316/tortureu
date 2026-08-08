package emit

import (
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
)

// spec: R-CLI-8

const kafkaFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://localhost:8080
egress:
  default: deny
  hosts:
    kafka:9092: { class: internal }
    # a queue fault's target is its TOPIC; config.Parse requires every fault
    # target to be a declared egress host or the SUT service.
    orders: { class: internal }
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
  - name: orders_poison
    at: peak
    for: 10s
    target: orders
    inject: { poison_pill: true, count: 3 }
  - name: orders_dupes
    at: peak
    for: 30s
    target: orders
    inject: { duplicate: 0.05 }
  - name: broker_slow
    at: peak
    for: 60s
    target: kafka:9092
    inject: { latency: 300ms }
assert:
  - http_req_duration: ["p(95)<500"]
`

func kafkaSystem() *detect.System {
	return &detect.System{Deps: []detect.Dep{{Name: "kafka", Type: "kafka", Address: "kafka:9092"}}}
}

// spec: R-CLI-8 — the broker address comes from detection (R-DET-4), never
// from a guessed default: an emitted script pointed at the wrong broker
// silently tests nothing.
func TestKafkaLoad_UsesDetectedBrokerAddress(t *testing.T) {
	out, err := KafkaLoad(mustParse(t, kafkaFixture), kafkaSystem())
	if err != nil {
		t.Fatalf("KafkaLoad: %v", err)
	}
	if !strings.Contains(out, `"kafka:9092"`) {
		t.Errorf("detected broker address kafka:9092 is not in the script:\n%s", out)
	}
}

// spec: R-CLI-8 — with no kafka dependency detected there is no address to
// write, so the emitter refuses and explains, the way sysbench/memtier do.
func TestKafkaLoad_RefusesWithoutDetectedBroker(t *testing.T) {
	cfg := mustParse(t, kafkaFixture)
	nilOut, err := KafkaLoad(cfg, nil)
	if err != nil {
		t.Fatalf("KafkaLoad(nil sys): %v", err)
	}
	if !strings.Contains(nilOut, "could not be detected") || strings.Contains(nilOut, "import") {
		t.Errorf("a nil system must produce a refusal, not a script:\n%s", nilOut)
	}
	emptyOut, err := KafkaLoad(cfg, &detect.System{})
	if err != nil {
		t.Fatalf("KafkaLoad(empty sys): %v", err)
	}
	if !strings.Contains(emptyOut, "dep:kafka") || strings.Contains(emptyOut, "import") {
		t.Errorf("no kafka dependency must produce a refusal naming dep:kafka:\n%s", emptyOut)
	}
}

// spec: R-CLI-8 — torture.yaml has no topic field. A topic must never be
// invented: producing into the wrong topic is a real side effect on
// someone's broker.
func TestKafkaLoad_NeverGuessesTopic(t *testing.T) {
	out, err := KafkaLoad(mustParse(t, kafkaFixture), kafkaSystem())
	if err != nil {
		t.Fatalf("KafkaLoad: %v", err)
	}
	if !strings.Contains(out, "KAFKA_TOPIC") {
		t.Errorf("the load topic must be a required __ENV.KAFKA_TOPIC:\n%s", out)
	}
	if !strings.Contains(out, "throw new Error") {
		t.Errorf("an unset topic must abort the script, not default to something:\n%s", out)
	}
}

// spec: R-CLI-8 — load.stages is an open arrival-rate model (R-CFG-6), and
// xk6-kafka runs inside k6, which expresses that model natively. Anything
// less faithful than ramping-arrival-rate would be a silent downgrade.
func TestKafkaLoad_StagesBecomeRampingArrivalRate(t *testing.T) {
	out, err := KafkaLoad(mustParse(t, kafkaFixture), kafkaSystem())
	if err != nil {
		t.Fatalf("KafkaLoad: %v", err)
	}
	for _, want := range []string{
		"ramping-arrival-rate",
		"startRate: 0",
		"{ target: 500, duration: '60s' }",
		"{ target: 500, duration: '180s' }",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// spec: R-CLI-8 — poison_pill and duplicate are broker-producer verbs
// (R-EXE-15, internal/queuefault). A Kafka producer CAN express them, so
// they are translated through queuefault.Translate rather than re-derived,
// and the fault's target names the topic.
func TestKafkaLoad_TranslatesQueueFaultsViaQueuefault(t *testing.T) {
	out, err := KafkaLoad(mustParse(t, kafkaFixture), kafkaSystem())
	if err != nil {
		t.Fatalf("KafkaLoad: %v", err)
	}
	if !strings.Contains(out, "orders_poison") || !strings.Contains(out, "3") {
		t.Errorf("poison_pill count: 3 was not translated:\n%s", out)
	}
	if !strings.Contains(out, "orders_dupes") || !strings.Contains(out, "0.05") {
		t.Errorf("duplicate rate 0.05 was not translated:\n%s", out)
	}
	if !strings.Contains(out, `topic: "orders"`) {
		t.Errorf("the queue fault's target is its topic and must be used verbatim:\n%s", out)
	}
}

// spec: R-CLI-8 — a fault a Kafka producer cannot express must be
// reported, never dropped.
func TestKafkaLoad_ReportsUntranslatableFaults(t *testing.T) {
	out, err := KafkaLoad(mustParse(t, kafkaFixture), kafkaSystem())
	if err != nil {
		t.Fatalf("KafkaLoad: %v", err)
	}
	if !strings.Contains(out, "broker_slow") || !strings.Contains(out, "not translated by kafka-load") {
		t.Errorf("the latency fault must be reported as not translated:\n%s", out)
	}
}

// spec: R-CLI-8 — nothing here is scheduled against the k6 phase clock
// (delegate tier): the injector scenarios carry no derived startTime.
func TestKafkaLoad_DoesNotScheduleFaults(t *testing.T) {
	out, err := KafkaLoad(mustParse(t, kafkaFixture), kafkaSystem())
	if err != nil {
		t.Fatalf("KafkaLoad: %v", err)
	}
	if strings.Contains(out, "startTime: '60s'") || strings.Contains(out, "startTime: '240s'") {
		t.Errorf("emit must not compute a fault start time from the phase clock:\n%s", out)
	}
	if !strings.Contains(out, "run this at peak") {
		t.Errorf("each fault's at: window must be surfaced as a comment for the human:\n%s", out)
	}
}

// spec: R-CLI-8 — the header records what was verified against a real
// xk6-kafka build and a real broker, and what was not.
func TestKafkaLoad_HeaderRecordsWhatWasVerified(t *testing.T) {
	if !strings.Contains(kafkaHeader, "VERIFICATION STATUS") {
		t.Fatal("kafkaHeader must carry a VERIFICATION STATUS block")
	}
	for _, want := range []string{"xk6-kafka", "redpanda"} {
		if !strings.Contains(kafkaHeader, want) {
			t.Errorf("kafkaHeader does not record %q as part of what was verified", want)
		}
	}
}

// spec: R-CLI-8 — registry.yaml spells this tool "tortureu emit
// kafka-load", and it needs detection for the broker address.
func TestKafkaLoad_RegisteredAsKafkaLoad(t *testing.T) {
	found := false
	for _, name := range Tools() {
		if name == "kafka-load" {
			found = true
		}
	}
	if !found {
		t.Fatalf("kafka-load is not registered; Tools() = %v", Tools())
	}
	if !NeedsSystem("kafka-load") {
		t.Error("kafka-load must declare needsSystem: the broker address comes from detection")
	}
	if _, err := Emit("kafka-load", mustParse(t, kafkaFixture), kafkaSystem()); err != nil {
		t.Errorf("Emit(\"kafka-load\"): %v", err)
	}
}
