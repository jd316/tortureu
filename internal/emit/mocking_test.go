package emit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/detect"
)

// spec: R-CLI-8
//
// `tortureu emit hoverfly` and `tortureu emit localstack` are the two
// dependency-virtualization delegate entries (registry.yaml domain 9).
// Neither is a fault injector: hoverfly stands in for a class: mock host
// and can hold one fault verb (latency) as a response delay; localstack
// stands in for AWS and holds none at all. Both must therefore say, per
// fault, what they do NOT translate rather than emitting a config that
// silently omits it.
const mockingFixture = `
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
    api.twilio.com: { class: mock, from: spec }
    metrics.vendor.io: { class: block }
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
    inject: { latency: 2s, jitter: 200ms }
  - name: stripe_down
    at: peak
    for: 10s
    target: api.stripe.com
    inject: { down: true }
  - name: cpu_squeeze
    at: peak
    for: 20s
    target: checkout-api
    inject: { cpu: 90%, workers: 4 }
  - name: pg_slow
    at: peak
    for: 5s
    target: postgres:5432
    inject: { latency: 300ms }
assert:
  - http_req_duration: ["p(95)<500"]
`

// mockingNoMockFixture has no class: mock host at all — the case where
// hoverfly has nothing legitimate to virtualize and must say so.
const mockingNoMockFixture = `
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
    - phase: peak
      hold: 100rps
      for: 30s
faults:
  - name: pg_slow
    at: peak
    for: 5s
    target: postgres:5432
    inject: { latency: 300ms }
assert:
  - http_req_duration: ["p(95)<500"]
`

// hoverflySimulationFromScript pulls the simulation JSON back out of the
// emitted shell script's heredoc and parses it. Parsing what we emit is
// this package's standing verification bar (see platform.go's header): a
// hand-templated document can look right and still be malformed.
func hoverflySimulationFromScript(t *testing.T, script string) map[string]any {
	t.Helper()
	const open = "<<'" + hoverflySimHeredoc + "'\n"
	_, after, ok := strings.Cut(script, open)
	if !ok {
		t.Fatalf("emitted script has no %s heredoc:\n%s", hoverflySimHeredoc, script)
	}
	body, _, ok := strings.Cut(after, "\n"+hoverflySimHeredoc+"\n")
	if !ok {
		t.Fatalf("emitted script's %s heredoc is not terminated:\n%s", hoverflySimHeredoc, script)
	}
	var sim map[string]any
	if err := json.Unmarshal([]byte(body), &sim); err != nil {
		t.Fatalf("emitted simulation is not valid JSON: %v\n%s", err, body)
	}
	return sim
}

// spec: R-CLI-8
func TestHoverfly_MockHostsBecomeSimulationPairs(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	out, err := Hoverfly(cfg)
	if err != nil {
		t.Fatalf("Hoverfly: %v", err)
	}
	sim := hoverflySimulationFromScript(t, out)
	data, _ := sim["data"].(map[string]any)
	pairs, _ := data["pairs"].([]any)
	if len(pairs) != 2 {
		t.Fatalf("want one pair per class: mock host (2), got %d", len(pairs))
	}
	if !strings.Contains(out, `"value": "api.stripe.com"`) || !strings.Contains(out, `"value": "api.twilio.com"`) {
		t.Errorf("both mock hosts must appear as destination matchers:\n%s", out)
	}
	// A class: internal or class: block host is not virtualized by a mock
	// provider; inventing a pair for it would fabricate egress policy.
	if strings.Contains(out, `"value": "metrics.vendor.io"`) || strings.Contains(out, `"value": "postgres"`) {
		t.Errorf("non-mock hosts must not become simulation pairs:\n%s", out)
	}
}

// spec: R-CLI-8
func TestHoverfly_LatencyOnMockHostBecomesGlobalDelay(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	out, err := Hoverfly(cfg)
	if err != nil {
		t.Fatalf("Hoverfly: %v", err)
	}
	sim := hoverflySimulationFromScript(t, out)
	data, _ := sim["data"].(map[string]any)
	ga, _ := data["globalActions"].(map[string]any)
	delays, _ := ga["delays"].([]any)
	if len(delays) != 1 {
		t.Fatalf("want 1 delay from the one latency fault on a mock host, got %d", len(delays))
	}
	d, _ := delays[0].(map[string]any)
	if got := d["delay"]; got != float64(2000) {
		t.Errorf("delay = %v, want 2000 (2s in ms, as internal/fault translates it)", got)
	}
	// The urlPattern is a regexp in hoverfly, so the dots must be escaped;
	// an unescaped "api.stripe.com" would also match "apixstripeycom".
	if got, _ := d["urlPattern"].(string); got != `api\.stripe\.com` {
		t.Errorf("urlPattern = %q, want the regexp-escaped host", got)
	}
}

// spec: R-CLI-8
func TestHoverfly_JitterIsReportedNotSilentlyDropped(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	out, err := Hoverfly(cfg)
	if err != nil {
		t.Fatalf("Hoverfly: %v", err)
	}
	if !strings.Contains(out, "jitter") || !strings.Contains(out, "200ms") {
		t.Errorf("the latency fault's jitter modifier must be reported as not applied:\n%s", out)
	}
}

// spec: R-CLI-8
func TestHoverfly_ReportsEveryUntranslatedFault(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	out, err := Hoverfly(cfg)
	if err != nil {
		t.Fatalf("Hoverfly: %v", err)
	}
	for _, name := range []string{"stripe_down", "cpu_squeeze", "pg_slow"} {
		if !strings.Contains(out, "fault \""+name+"\"") {
			t.Errorf("fault %q must be reported, not dropped:\n%s", name, out)
		}
	}
	if !strings.Contains(out, "not translated by hoverfly emit") {
		t.Errorf("untranslated faults must say so in the tool's own words:\n%s", out)
	}
}

// spec: R-CLI-8
func TestHoverfly_NoMockHostRefusesRatherThanInventingOne(t *testing.T) {
	cfg := mustParse(t, mockingNoMockFixture)
	out, err := Hoverfly(cfg)
	if err != nil {
		t.Fatalf("Hoverfly: %v", err)
	}
	if strings.Contains(out, hoverflySimHeredoc) {
		t.Errorf("with no class: mock host there is nothing to virtualize, so no simulation may be emitted:\n%s", out)
	}
	if !strings.Contains(out, "class: mock") || !strings.Contains(out, "nothing to emit") {
		t.Errorf("the refusal must name the missing input:\n%s", out)
	}
	if !strings.Contains(out, "fault \"pg_slow\"") {
		t.Errorf("faults must still be reported when nothing is emitted:\n%s", out)
	}
}

// spec: R-CLI-8
//
// The emitted script must carry the commands that were actually run
// against a live hoverfly during implementation, not a plausible-looking
// variant: the admin-API import below is the path this file's header
// claims was verified.
func TestHoverfly_EmitsTheVerifiedRunAndImportCommands(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	out, err := Hoverfly(cfg)
	if err != nil {
		t.Fatalf("Hoverfly: %v", err)
	}
	for _, want := range []string{
		"docker run -d --name",
		"spectolabs/hoverfly:v1.12.11",
		"-listen-on-host=0.0.0.0",
		"/api/v2/simulation",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("emitted script must contain %q:\n%s", want, out)
		}
	}
}

// spec: R-CLI-8
//
// torture.yaml carries no response bodies, so the emitted simulation
// answers with an empty one. That is a real limitation of the input, and
// the output must say so rather than let a user discover it when their
// SUT fails to parse an invented payload.
func TestHoverfly_HeaderDisclosesEmptyResponseBodies(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	out, err := Hoverfly(cfg)
	if err != nil {
		t.Fatalf("Hoverfly: %v", err)
	}
	if !strings.Contains(out, "no response bodies") {
		t.Errorf("header must disclose that bodies are not derived from torture.yaml:\n%s", out)
	}
}

// ---- localstack ---------------------------------------------------------

// spec: R-CLI-8
func TestLocalStack_NilSystemRefusesRatherThanGuessing(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	out, err := LocalStack(cfg, nil)
	if err != nil {
		t.Fatalf("LocalStack: %v", err)
	}
	if !strings.Contains(out, "could not be detected") || !strings.Contains(out, "nothing to emit") {
		t.Errorf("a nil *detect.System must be reported as our missing input, not as an absent dependency:\n%s", out)
	}
	if strings.Contains(out, "docker run") {
		t.Errorf("nothing runnable may be emitted without detection:\n%s", out)
	}
}

// spec: R-CLI-8
func TestLocalStack_NoAWSEvidenceReportsThePredicateAsFalse(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	sys := &detect.System{Coverage: detect.Coverage{AWS: detect.FactFalse}}
	out, err := LocalStack(cfg, sys)
	if err != nil {
		t.Fatalf("LocalStack: %v", err)
	}
	if !strings.Contains(out, "dep:aws") || !strings.Contains(out, "nothing to emit") {
		t.Errorf("with no AWS evidence the registry predicate must be reported as not holding:\n%s", out)
	}
}

// spec: R-COV-6
//
// FactUnknown means the manifest format was unsupported, so we could not
// check — distinct from "checked, and there is no AWS SDK". Collapsing the
// two would report an undetermined fact as a verified absence.
func TestLocalStack_UndeterminedAWSFactIsNotReportedAsAbsent(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	sys := &detect.System{Coverage: detect.Coverage{AWS: detect.FactUnknown}}
	out, err := LocalStack(cfg, sys)
	if err != nil {
		t.Fatalf("LocalStack: %v", err)
	}
	if !strings.Contains(out, "undetermined") {
		t.Errorf("an undetermined platform:aws fact must be reported as undetermined:\n%s", out)
	}
	if strings.Contains(out, "no AWS SDK") {
		t.Errorf("undetermined must not be phrased as a verified absence:\n%s", out)
	}
}

// spec: R-CLI-8
func TestLocalStack_ServicesComeFromDetectedClientsOnly(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	sys := &detect.System{
		Coverage: detect.Coverage{AWS: detect.FactTrue},
		Deps: []detect.Dep{
			{Name: "sqs", Type: "sqs"},
			{Name: "s3", Type: "s3"},
			{Name: "postgres", Type: "postgresql", Address: "postgres:5432"},
		},
	}
	out, err := LocalStack(cfg, sys)
	if err != nil {
		t.Fatalf("LocalStack: %v", err)
	}
	if !strings.Contains(out, "SERVICES=s3,sqs") {
		t.Errorf("SERVICES must be exactly the detected AWS service clients, sorted:\n%s", out)
	}
	if strings.Contains(out, "postgresql") {
		t.Errorf("a non-AWS dependency must not leak into SERVICES:\n%s", out)
	}
}

// spec: R-CLI-8
//
// torture.yaml carries no AWS region, so the script must demand one from
// the caller instead of substituting a default that would silently send
// every emulated call to the wrong region's namespace.
func TestLocalStack_RegionIsDemandedNotGuessed(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	sys := &detect.System{Coverage: detect.Coverage{AWS: detect.FactTrue}}
	out, err := LocalStack(cfg, sys)
	if err != nil {
		t.Fatalf("LocalStack: %v", err)
	}
	if !strings.Contains(out, `: "${AWS_REGION:?`) {
		t.Errorf("the script must fail closed when AWS_REGION is unset:\n%s", out)
	}
	if strings.Contains(out, "AWS_DEFAULT_REGION=us-east-1") {
		t.Errorf("no region may be baked in:\n%s", out)
	}
}

// spec: R-CLI-8
func TestLocalStack_ReportsEveryFaultAsUntranslated(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	sys := &detect.System{Coverage: detect.Coverage{AWS: detect.FactTrue}}
	out, err := LocalStack(cfg, sys)
	if err != nil {
		t.Fatalf("LocalStack: %v", err)
	}
	for _, f := range cfg.Faults {
		if !strings.Contains(out, "fault \""+f.Name+"\"") {
			t.Errorf("fault %q must be reported as untranslated, not dropped:\n%s", f.Name, out)
		}
	}
	if !strings.Contains(out, "not translated by localstack emit") {
		t.Errorf("localstack injects no faults and must say so per fault:\n%s", out)
	}
	if !strings.Contains(out, "tortureu emit pumba") {
		t.Errorf("the output should name the tools that DO inject these faults:\n%s", out)
	}
}

// spec: R-CLI-8
func TestLocalStack_ExistingComposeServiceIsNamedNotDuplicated(t *testing.T) {
	cfg := mustParse(t, mockingFixture)
	sys := &detect.System{
		Coverage: detect.Coverage{AWS: detect.FactTrue},
		Deps:     []detect.Dep{{Name: "localstack", Type: "aws", Address: "localstack:4566"}},
	}
	out, err := LocalStack(cfg, sys)
	if err != nil {
		t.Fatalf("LocalStack: %v", err)
	}
	if !strings.Contains(out, "localstack:4566") {
		t.Errorf("the already-detected emulator's own address must be used:\n%s", out)
	}
	if strings.Contains(out, "docker run -d --name") {
		t.Errorf("a second emulator must not be started alongside the compose one:\n%s", out)
	}
}
