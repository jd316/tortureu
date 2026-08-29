package emit

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/detect"
	"gopkg.in/yaml.v3"
)

// spec: R-CLI-8
//
// `tortureu emit testcontainers` and `tortureu emit kind` are the two
// environment-provisioning delegate entries (registry.yaml domain 10).
// Neither injects a fault: they stand the environment up. So both must
// report every fault in torture.yaml as untranslated and name the emitter
// that does translate it, rather than emitting scaffolding that looks like
// it covers the scenario.
const envprovFixture = `
version: 0
target:
  compose: ./deploy/docker-compose.yml
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
      hold: 200rps
      for: 60s
faults:
  - name: pg_slow
    at: peak
    for: 30s
    target: postgres:5432
    inject: { latency: 300ms }
  - name: api_kill
    at: peak
    target: checkout-api
    inject: { kill: true }
assert:
  - http_req_duration: ["p(95)<500"]
`

// envprovNoPortFixture's base_url carries no port, so kind has nothing to
// derive a host port mapping from.
const envprovNoPortFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://checkout.local
egress:
  default: deny
  hosts:
    postgres:5432: { class: internal }
load:
  engine: k6
  model: arrival_rate
  stages:
    - phase: peak
      hold: 200rps
      for: 60s
faults:
  - name: pg_slow
    at: peak
    for: 30s
    target: postgres:5432
    inject: { latency: 300ms }
assert:
  - http_req_duration: ["p(95)<500"]
`

func envprovGoSystem() *detect.System {
	return &detect.System{Lang: "go", SUT: "checkout-api"}
}

func envprovK8sSystem() *detect.System {
	return &detect.System{SUT: "checkout-api", Coverage: detect.Coverage{K8s: true}}
}

// spec: R-CLI-8
//
// The emitted helper is Go source, so the bar is that Go itself accepts
// it: a helper that does not parse is not a handoff, it is a snippet.
func TestTestcontainers_EmittedHelperParsesAsGo(t *testing.T) {
	cfg := mustParse(t, envprovFixture)
	out, err := Testcontainers(cfg, envprovGoSystem())
	if err != nil {
		t.Fatalf("Testcontainers: %v", err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "tortureu_env_test.go", out, parser.AllErrors); err != nil {
		t.Fatalf("emitted helper does not parse as Go: %v\n%s", err, out)
	}
}

// spec: R-CLI-8
func TestTestcontainers_UsesComposePathAndServiceFromConfig(t *testing.T) {
	cfg := mustParse(t, envprovFixture)
	out, err := Testcontainers(cfg, envprovGoSystem())
	if err != nil {
		t.Fatalf("Testcontainers: %v", err)
	}
	if !strings.Contains(out, `"./deploy/docker-compose.yml"`) {
		t.Errorf("the compose path must come from target.compose:\n%s", out)
	}
	if !strings.Contains(out, `"checkout-api"`) {
		t.Errorf("the service must come from target.service:\n%s", out)
	}
	if !strings.Contains(out, "github.com/testcontainers/testcontainers-go/modules/compose") {
		t.Errorf("the helper must import the compose module it calls:\n%s", out)
	}
}

// spec: R-CLI-8
func TestTestcontainers_NonGoLanguageRefusesInsteadOfEmittingGo(t *testing.T) {
	cfg := mustParse(t, envprovFixture)
	out, err := Testcontainers(cfg, &detect.System{Lang: "python"})
	if err != nil {
		t.Fatalf("Testcontainers: %v", err)
	}
	if strings.Contains(out, "func Test") {
		t.Errorf("a Go helper must not be emitted for a python repo:\n%s", out)
	}
	if !strings.Contains(out, "python") || !strings.Contains(out, "nothing to emit") {
		t.Errorf("the refusal must name the detected language:\n%s", out)
	}
}

// spec: R-CLI-8
func TestTestcontainers_NilSystemRefuses(t *testing.T) {
	cfg := mustParse(t, envprovFixture)
	out, err := Testcontainers(cfg, nil)
	if err != nil {
		t.Fatalf("Testcontainers: %v", err)
	}
	if !strings.Contains(out, "could not be detected") || !strings.Contains(out, "nothing to emit") {
		t.Errorf("a nil *detect.System must be reported as our missing input:\n%s", out)
	}
}

// spec: R-CLI-8
func TestTestcontainers_ReportsEveryFaultAsUntranslated(t *testing.T) {
	cfg := mustParse(t, envprovFixture)
	out, err := Testcontainers(cfg, envprovGoSystem())
	if err != nil {
		t.Fatalf("Testcontainers: %v", err)
	}
	for _, f := range cfg.Faults {
		if !strings.Contains(out, "fault \""+f.Name+"\"") {
			t.Errorf("fault %q must be reported as untranslated, not dropped:\n%s", f.Name, out)
		}
	}
	if !strings.Contains(out, "not translated by testcontainers emit") {
		t.Errorf("testcontainers provisions, it does not inject — say so per fault:\n%s", out)
	}
}

// ---- kind ---------------------------------------------------------------

// kindConfigFromScript pulls the kind cluster config back out of the
// emitted script's heredoc and parses it, so a malformed document fails
// here rather than at `kind create cluster` time.
func kindConfigFromScript(t *testing.T, script string) map[string]any {
	t.Helper()
	const open = "<<'" + kindConfigHeredoc + "'\n"
	_, after, ok := strings.Cut(script, open)
	if !ok {
		t.Fatalf("emitted script has no %s heredoc:\n%s", kindConfigHeredoc, script)
	}
	body, _, ok := strings.Cut(after, "\n"+kindConfigHeredoc+"\n")
	if !ok {
		t.Fatalf("emitted script's %s heredoc is not terminated:\n%s", kindConfigHeredoc, script)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("emitted kind config is not valid YAML: %v\n%s", err, body)
	}
	return doc
}

// spec: R-CLI-8
func TestKind_ConfigCarriesTheRequiredClusterFields(t *testing.T) {
	cfg := mustParse(t, envprovFixture)
	out, err := Kind(cfg, envprovK8sSystem())
	if err != nil {
		t.Fatalf("Kind: %v", err)
	}
	doc := kindConfigFromScript(t, out)
	if doc["kind"] != "Cluster" {
		t.Errorf("kind = %v, want Cluster", doc["kind"])
	}
	if doc["apiVersion"] != "kind.x-k8s.io/v1alpha4" {
		t.Errorf("apiVersion = %v, want kind.x-k8s.io/v1alpha4", doc["apiVersion"])
	}
	nodes, _ := doc["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("want exactly one control-plane node, got %d", len(nodes))
	}
	node, _ := nodes[0].(map[string]any)
	if node["role"] != "control-plane" {
		t.Errorf("node role = %v, want control-plane", node["role"])
	}
	maps, _ := node["extraPortMappings"].([]any)
	if len(maps) != 1 {
		t.Fatalf("want one port mapping derived from target.base_url, got %d", len(maps))
	}
	m, _ := maps[0].(map[string]any)
	if m["hostPort"] != 8080 {
		t.Errorf("hostPort = %v, want 8080 from target.base_url", m["hostPort"])
	}
	if m["containerPort"] != kindNodePort {
		t.Errorf("containerPort = %v, want the NodePort this emit documents (%d)", m["containerPort"], kindNodePort)
	}
}

// spec: R-CLI-8
func TestKind_BaseURLWithoutPortOmitsTheMappingAndSaysWhy(t *testing.T) {
	cfg := mustParse(t, envprovNoPortFixture)
	out, err := Kind(cfg, envprovK8sSystem())
	if err != nil {
		t.Fatalf("Kind: %v", err)
	}
	doc := kindConfigFromScript(t, out)
	nodes, _ := doc["nodes"].([]any)
	node, _ := nodes[0].(map[string]any)
	if _, has := node["extraPortMappings"]; has {
		t.Errorf("no port in base_url means no mapping may be invented:\n%s", out)
	}
	if !strings.Contains(out, "no explicit port") {
		t.Errorf("the omission must be explained in the output:\n%s", out)
	}
}

// spec: R-CLI-8
func TestKind_NoKubernetesEvidenceRefuses(t *testing.T) {
	cfg := mustParse(t, envprovFixture)
	out, err := Kind(cfg, &detect.System{})
	if err != nil {
		t.Fatalf("Kind: %v", err)
	}
	if strings.Contains(out, kindConfigHeredoc) {
		t.Errorf("without platform:k8s there is no cluster to provision:\n%s", out)
	}
	if !strings.Contains(out, "platform:k8s") || !strings.Contains(out, "nothing to emit") {
		t.Errorf("the refusal must name the registry predicate that does not hold:\n%s", out)
	}
}

// spec: R-CLI-8
func TestKind_NilSystemRefuses(t *testing.T) {
	cfg := mustParse(t, envprovFixture)
	out, err := Kind(cfg, nil)
	if err != nil {
		t.Fatalf("Kind: %v", err)
	}
	if !strings.Contains(out, "could not be detected") || !strings.Contains(out, "nothing to emit") {
		t.Errorf("a nil *detect.System must be reported as our missing input:\n%s", out)
	}
}

// spec: R-CLI-8
func TestKind_ReportsEveryFaultAsUntranslatedAndNamesTheClusterInjector(t *testing.T) {
	cfg := mustParse(t, envprovFixture)
	out, err := Kind(cfg, envprovK8sSystem())
	if err != nil {
		t.Fatalf("Kind: %v", err)
	}
	for _, f := range cfg.Faults {
		if !strings.Contains(out, "fault \""+f.Name+"\"") {
			t.Errorf("fault %q must be reported as untranslated, not dropped:\n%s", f.Name, out)
		}
	}
	if !strings.Contains(out, "not translated by kind emit") {
		t.Errorf("kind provisions the cluster, it injects nothing — say so per fault:\n%s", out)
	}
	if !strings.Contains(out, "tortureu emit chaosmesh") {
		t.Errorf("the in-cluster fault path must be named:\n%s", out)
	}
}

// spec: R-CLI-8
//
// The script must carry the exact commands run against the real kind
// binary during implementation, so the verification claim in the file
// header stays bound to what is emitted.
func TestKind_EmitsTheVerifiedCommands(t *testing.T) {
	cfg := mustParse(t, envprovFixture)
	out, err := Kind(cfg, envprovK8sSystem())
	if err != nil {
		t.Fatalf("Kind: %v", err)
	}
	for _, want := range []string{
		"kind create cluster --config",
		"kubectl cluster-info --context kind-",
		"kind delete cluster --name",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("emitted script must contain %q:\n%s", want, out)
		}
	}
}
