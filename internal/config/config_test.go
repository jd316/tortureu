// Package config_test drives internal/config through TDD, one requirement at a time.
package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/config"
)

// spec: R-CFG-1
func TestExampleConfigParsesAndValidatesClean(t *testing.T) {
	raw, err := os.ReadFile("../../torture.example.yaml")
	if err != nil {
		t.Fatalf("reading torture.example.yaml: %v", err)
	}
	if _, err := config.Parse(raw); err != nil {
		t.Fatalf("torture.example.yaml must parse and validate clean, got: %v", err)
	}
}

// spec: R-CFG-3
func TestTargetRequiresComposeAndService(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml }
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for missing target.service, got nil")
	}
	if !strings.Contains(err.Error(), "target.service") {
		t.Fatalf("expected error to name target.service, got: %v", err)
	}
}

// spec: R-CFG-3
func TestTargetRequiresCompose(t *testing.T) {
	src := `
version: 0
target: { service: api }
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for missing target.compose, got nil")
	}
	if !strings.Contains(err.Error(), "target.compose") {
		t.Fatalf("expected error to name target.compose, got: %v", err)
	}
}

// spec: R-CFG-4
func TestEgressDefaultMustBeDeny(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
egress:
  default: allow
  hosts: {}
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for egress.default != deny, got nil")
	}
	if !strings.Contains(err.Error(), "egress.default") {
		t.Fatalf("expected error to name egress.default, got: %v", err)
	}
}

// spec: R-CFG-5
func TestEgressMockClassRequiresFrom(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
egress:
  default: deny
  hosts:
    api.stripe.com: { class: mock }
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for mock class missing from:, got nil")
	}
	if !strings.Contains(err.Error(), "api.stripe.com") || !strings.Contains(err.Error(), "from") {
		t.Fatalf("expected error to name host and 'from', got: %v", err)
	}
}

// spec: R-CFG-5
func TestEgressRealClassRequiresMaxRPS(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
egress:
  default: deny
  hosts:
    sandbox.partner.com: { class: real }
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for real class missing max_rps, got nil")
	}
	if !strings.Contains(err.Error(), "sandbox.partner.com") || !strings.Contains(err.Error(), "max_rps") {
		t.Fatalf("expected error to name host and 'max_rps', got: %v", err)
	}
}

// spec: R-CFG-6
func TestLoadModelMustBeArrivalRate(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
load:
  model: closed
  stages:
    - phase: peak
      hold: 500rps
      for: 60s
assert:
  - http_req_duration: ["p(95)<500"]
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for closed load model, got nil")
	}
	if !strings.Contains(err.Error(), "load.model") {
		t.Fatalf("expected error to name load.model, got: %v", err)
	}
}

// spec: R-CFG-7
func TestLoadStagePhaseMustBeUnique(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
load:
  model: arrival_rate
  stages:
    - phase: peak
      hold: 500rps
      for: 60s
    - phase: peak
      to: 100rps
      over: 10s
assert:
  - http_req_duration: ["p(95)<500"]
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for duplicate phase name, got nil")
	}
	if !strings.Contains(err.Error(), "peak") {
		t.Fatalf("expected error to name the duplicate phase, got: %v", err)
	}
}

// spec: R-CFG-8
func TestLoadStageMustHaveExactlyOneOfToOrHold(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
load:
  model: arrival_rate
  stages:
    - phase: peak
      to: 500rps
      hold: 500rps
      for: 60s
assert:
  - http_req_duration: ["p(95)<500"]
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for stage with both to: and hold:, got nil")
	}
	if !strings.Contains(err.Error(), "peak") {
		t.Fatalf("expected error to name the offending stage, got: %v", err)
	}
}

// spec: R-CFG-8
func TestLoadStageMustHaveAtLeastOneOfToOrHold(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
load:
  model: arrival_rate
  stages:
    - phase: peak
      for: 60s
assert:
  - http_req_duration: ["p(95)<500"]
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for stage with neither to: nor hold:, got nil")
	}
	if !strings.Contains(err.Error(), "peak") {
		t.Fatalf("expected error to name the offending stage, got: %v", err)
	}
}

// spec: R-CFG-9
func TestScenarioFlowEntriesMustBeMappings(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
load:
  model: arrival_rate
  stages:
    - phase: peak
      hold: 500rps
      for: 60s
  scenarios:
    - name: browse
      weight: 100
      flow:
        - "GET /products"
assert:
  - http_req_duration: ["p(95)<500"]
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for bare string flow entry, got nil")
	}
	if !strings.Contains(err.Error(), "flow") {
		t.Fatalf("expected error to mention flow, got: %v", err)
	}
}

// spec: R-CFG-9
func TestScenarioFlowEntryRequiresMethodAndPath(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
load:
  model: arrival_rate
  stages:
    - phase: peak
      hold: 500rps
      for: 60s
  scenarios:
    - name: checkout
      weight: 100
      flow:
        - { capture: order_id }
assert:
  - http_req_duration: ["p(95)<500"]
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for flow entry missing method/path, got nil")
	}
	if !strings.Contains(err.Error(), "method") || !strings.Contains(err.Error(), "path") {
		t.Fatalf("expected error to mention method and path, got: %v", err)
	}
}

func baseWithLoadAndAssert(faultsBlock string) string {
	return `
version: 0
target: { compose: ./docker-compose.yml, service: checkout-api }
egress:
  default: deny
  hosts:
    postgres:5432: { class: internal }
load:
  model: arrival_rate
  stages:
    - phase: peak
      hold: 500rps
      for: 60s
    - phase: spike
      to: 5000rps
      over: 10s
` + faultsBlock + `
assert:
  - http_req_duration: ["p(95)<500"]
`
}

// spec: R-CFG-10
func TestFaultRequiresAtTargetInject(t *testing.T) {
	src := baseWithLoadAndAssert(`
faults:
  - name: pg_slow
    target: postgres:5432
    inject: { latency: 300ms }
`)
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for fault missing at:, got nil")
	}
	if !strings.Contains(err.Error(), "pg_slow") || !strings.Contains(err.Error(), "at") {
		t.Fatalf("expected error to name fault pg_slow and 'at', got: %v", err)
	}
}

// spec: R-CFG-11
func TestFaultAtGrammar(t *testing.T) {
	cases := []struct {
		name string
		at   string
		ok   bool
	}{
		{"bare phase", "peak", true},
		{"phase plus offset", "peak+30s", true},
		{"absolute", "t=90s", true},
		{"garbage", "whenever", false},
	}
	for _, c := range cases {
		src := baseWithLoadAndAssert(`
faults:
  - name: f1
    at: ` + c.at + `
    target: postgres:5432
    inject: { latency: 300ms }
`)
		_, err := config.Parse([]byte(src))
		if c.ok && err != nil {
			t.Errorf("%s: at: %q expected to parse, got error: %v", c.name, c.at, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: at: %q expected error, got nil", c.name, c.at)
		}
	}
}

// spec: R-CFG-12
func TestFaultAtUndeclaredPhaseIsError(t *testing.T) {
	src := baseWithLoadAndAssert(`
faults:
  - name: f1
    at: ghost_phase
    target: postgres:5432
    inject: { latency: 300ms }
`)
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for undeclared phase, got nil")
	}
	if !strings.Contains(err.Error(), "ghost_phase") {
		t.Fatalf("expected error to name the undeclared phase, got: %v", err)
	}
}

// spec: R-CFG-13
func TestFaultTargetMustBeKnown(t *testing.T) {
	src := baseWithLoadAndAssert(`
faults:
  - name: f1
    at: peak
    target: unknown-host:1234
    inject: { latency: 300ms }
`)
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for unknown fault target, got nil")
	}
	if !strings.Contains(err.Error(), "unknown-host:1234") {
		t.Fatalf("expected error to name the unknown target, got: %v", err)
	}
}

// spec: R-CFG-14
func TestFaultInjectExactlyOneVerb(t *testing.T) {
	src := baseWithLoadAndAssert(`
faults:
  - name: f1
    at: peak
    target: postgres:5432
    inject: { latency: 300ms, down: true }
`)
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for two verbs in one inject, got nil")
	}
	if !strings.Contains(err.Error(), "f1") {
		t.Fatalf("expected error to name fault f1, got: %v", err)
	}
}

// spec: R-CFG-14
func TestFaultInjectModifierMustBelongToVerb(t *testing.T) {
	cases := []struct {
		name   string
		inject string
	}{
		{"jitter without latency", "{ down: true, jitter: 50ms }"},
		{"workers on down", "{ down: true, workers: 4 }"},
		{"status on latency", "{ latency: 300ms, status: 503 }"},
	}
	for _, c := range cases {
		src := baseWithLoadAndAssert(`
faults:
  - name: f1
    at: peak
    target: postgres:5432
    inject: ` + c.inject + `
`)
		_, err := config.Parse([]byte(src))
		if err == nil {
			t.Errorf("%s: expected error for modifier not owned by verb, got nil", c.name)
		} else if !strings.Contains(err.Error(), "f1") {
			t.Errorf("%s: expected error to name fault f1, got: %v", c.name, err)
		}
	}
}

// spec: R-CFG-14
func TestFaultInjectModifierAllowedForOwningVerb(t *testing.T) {
	src := baseWithLoadAndAssert(`
faults:
  - name: f1
    at: peak
    target: postgres:5432
    inject: { latency: 300ms, jitter: 50ms }
`)
	if _, err := config.Parse([]byte(src)); err != nil {
		t.Fatalf("expected jitter to be a legal modifier of latency, got error: %v", err)
	}
}

// spec: R-CFG-23
func TestFaultDuplicateRangeChecked(t *testing.T) {
	cases := []struct {
		name  string
		value string
		ok    bool
	}{
		{"above range — the motivating 500% case", "5", false},
		{"below range", "-0.1", false},
		{"lower boundary 0.0 is legal", "0.0", true},
		{"upper boundary 1.0 is legal", "1.0", true},
	}
	for _, c := range cases {
		src := baseWithLoadAndAssert(`
faults:
  - name: f1
    at: peak
    target: postgres:5432
    inject: { duplicate: ` + c.value + ` }
`)
		_, err := config.Parse([]byte(src))
		if c.ok && err != nil {
			t.Errorf("%s: duplicate: %s expected to validate, got error: %v", c.name, c.value, err)
		}
		if !c.ok {
			if err == nil {
				t.Errorf("%s: duplicate: %s expected error, got nil", c.name, c.value)
			} else if !strings.Contains(err.Error(), "f1") || !strings.Contains(err.Error(), "duplicate") || !strings.Contains(err.Error(), "0") || !strings.Contains(err.Error(), "1") {
				t.Errorf("%s: expected error to name fault, modifier, and range, got: %v", c.name, err)
			}
		}
	}
}

// spec: R-CFG-23
func TestFaultErrorRateRangeChecked(t *testing.T) {
	cases := []struct {
		name  string
		value string
		ok    bool
	}{
		{"above range", "1.5", false},
		{"below range", "-0.01", false},
		{"lower boundary 0.0 is legal", "0.0", true},
		{"upper boundary 1.0 is legal", "1.0", true},
	}
	for _, c := range cases {
		src := baseWithLoadAndAssert(`
faults:
  - name: f1
    at: spike
    target: api.stripe.com
    inject: { error_rate: ` + c.value + ` }
`)
		src = strings.Replace(src, "postgres:5432: { class: internal }", "postgres:5432: { class: internal }\n    api.stripe.com: { class: mock, from: capture }", 1)
		_, err := config.Parse([]byte(src))
		if c.ok && err != nil {
			t.Errorf("%s: error_rate: %s expected to validate, got error: %v", c.name, c.value, err)
		}
		if !c.ok {
			if err == nil {
				t.Errorf("%s: error_rate: %s expected error, got nil", c.name, c.value)
			} else if !strings.Contains(err.Error(), "f1") || !strings.Contains(err.Error(), "error_rate") {
				t.Errorf("%s: expected error to name fault and modifier, got: %v", c.name, err)
			}
		}
	}
}

// spec: R-CFG-23
func TestFaultPoisonPillCountMustBePositiveInteger(t *testing.T) {
	cases := []struct {
		name  string
		value string
		ok    bool
	}{
		{"zero is illegal", "0", false},
		{"negative is illegal", "-1", false},
		{"one is the legal boundary", "1", true},
	}
	for _, c := range cases {
		src := baseWithLoadAndAssert(`
faults:
  - name: f1
    at: peak
    target: postgres:5432
    inject: { poison_pill: true, count: ` + c.value + ` }
`)
		_, err := config.Parse([]byte(src))
		if c.ok && err != nil {
			t.Errorf("%s: count: %s expected to validate, got error: %v", c.name, c.value, err)
		}
		if !c.ok {
			if err == nil {
				t.Errorf("%s: count: %s expected error, got nil", c.name, c.value)
			} else if !strings.Contains(err.Error(), "f1") || !strings.Contains(err.Error(), "count") {
				t.Errorf("%s: expected error to name fault and modifier, got: %v", c.name, err)
			}
		}
	}
}

// spec: R-CFG-23
func TestFaultWorkersMustBePositiveInteger(t *testing.T) {
	cases := []struct {
		name  string
		value string
		ok    bool
	}{
		{"zero is illegal", "0", false},
		{"negative is illegal", "-4", false},
		{"one is the legal boundary", "1", true},
	}
	for _, c := range cases {
		src := baseWithLoadAndAssert(`
faults:
  - name: f1
    at: peak
    target: checkout-api
    inject: { cpu: 90%, workers: ` + c.value + ` }
`)
		_, err := config.Parse([]byte(src))
		if c.ok && err != nil {
			t.Errorf("%s: workers: %s expected to validate, got error: %v", c.name, c.value, err)
		}
		if !c.ok {
			if err == nil {
				t.Errorf("%s: workers: %s expected error, got nil", c.name, c.value)
			} else if !strings.Contains(err.Error(), "f1") || !strings.Contains(err.Error(), "workers") {
				t.Errorf("%s: expected error to name fault and modifier, got: %v", c.name, err)
			}
		}
	}
}

// spec: R-CFG-15
func TestFaultPauseKillGracefulAreDistinctVerbs(t *testing.T) {
	src := baseWithLoadAndAssert(`
faults:
  - name: f1
    at: peak
    target: checkout-api
    inject: { pause: true }
  - name: f2
    at: peak
    target: checkout-api
    inject: { kill: true }
`)
	cfg, err := config.Parse([]byte(src))
	if err != nil {
		t.Fatalf("expected pause and kill to be accepted as distinct verbs, got error: %v", err)
	}
	if cfg.Faults[0].Verb != "pause" || cfg.Faults[1].Verb != "kill" {
		t.Fatalf("expected distinct verbs pause/kill, got %q/%q", cfg.Faults[0].Verb, cfg.Faults[1].Verb)
	}
}

// spec: R-CFG-19
func TestAssertAbsentIsError(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for absent assert:, got nil")
	}
	if !strings.Contains(err.Error(), "assert") {
		t.Fatalf("expected error to mention assert, got: %v", err)
	}
}

// spec: R-CFG-19
func TestAssertEmptyIsError(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
assert: []
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for empty assert:, got nil")
	}
	if !strings.Contains(err.Error(), "assert") {
		t.Fatalf("expected error to mention assert, got: %v", err)
	}
}

// spec: R-CFG-16
func TestAssertK6ThresholdVerbatim(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
assert:
  - http_req_duration: ["p(95)<500", "p(99)<1500"]
`
	cfg, err := config.Parse([]byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := cfg.Assert[0]["http_req_duration"].([]any)
	if !ok || got[0] != "p(95)<500" || got[1] != "p(99)<1500" {
		t.Fatalf("expected verbatim threshold expressions preserved, got: %#v", cfg.Assert[0])
	}
}

// spec: R-CFG-17
func TestAssertPromqlEntryAccepted(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
assert:
  - promql: 'sum(rate(app_retries_total[30s])) < 100'
`
	cfg, err := config.Parse([]byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Assert[0]["promql"] != "sum(rate(app_retries_total[30s])) < 100" {
		t.Fatalf("expected promql entry preserved, got: %#v", cfg.Assert[0])
	}
}

// spec: R-CFG-18
func TestAssertSQLEntryAccepted(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
assert:
  - sql: 'SELECT count(*) FROM orders WHERE status IS NULL'
`
	cfg, err := config.Parse([]byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Assert[0]["sql"] != "SELECT count(*) FROM orders WHERE status IS NULL" {
		t.Fatalf("expected sql entry preserved, got: %#v", cfg.Assert[0])
	}
}

// spec: R-CFG-21
func TestResetCommandDefaultsWhenAbsent(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
assert:
  - http_req_duration: ["p(95)<500"]
`
	cfg, err := config.Parse([]byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "docker compose down -v && docker compose up -d --wait"
	if cfg.Reset.Command != want {
		t.Fatalf("expected default reset command %q, got %q", want, cfg.Reset.Command)
	}
}

// spec: R-CFG-21
func TestResetCommandUserSuppliedOverridesDefault(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
reset:
  command: ./scripts/custom-reset.sh
assert:
  - http_req_duration: ["p(95)<500"]
`
	cfg, err := config.Parse([]byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Reset.Command != "./scripts/custom-reset.sh" {
		t.Fatalf("expected user-supplied reset command preserved, got %q", cfg.Reset.Command)
	}
}

// spec: R-CFG-1
func TestNoIncludeMechanism(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
include: ./other.yaml
assert:
  - http_req_duration: ["p(95)<500"]
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error: torture.yaml is a single flat file with no include mechanism")
	}
	if !strings.Contains(err.Error(), "include") {
		t.Fatalf("expected error to name the offending key, got: %v", err)
	}
}

// spec: R-CFG-2
func TestUnknownTopLevelKeyIsError(t *testing.T) {
	src := `
version: 0
target: { compose: ./docker-compose.yml, service: api }
asert:
  - http_req_duration: ["p(95)<500"]
`
	_, err := config.Parse([]byte(src))
	if err == nil {
		t.Fatal("expected error for unknown top-level key, got nil")
	}
	if !strings.Contains(err.Error(), "asert") {
		t.Fatalf("expected error to name the offending key %q, got: %v", "asert", err)
	}
}
