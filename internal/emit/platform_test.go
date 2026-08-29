package emit

import (
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/config"
	"gopkg.in/yaml.v3"
)

// platformFixture adds mem/pause faults (unsupported by chaosmesh emit) to
// netemFixture's set, so skip behaviour has something real to skip.
const platformFixture = `
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
  - name: mem_squeeze
    at: peak
    for: 10s
    target: checkout-api
    inject: { mem: 80% }
  - name: api_pause
    at: peak
    for: 5s
    target: checkout-api
    inject: { pause: true }
assert:
  - http_req_duration: ["p(95)<500"]
`

// spec: R-CLI-8
// `tortureu emit chaosmesh` translates a latency fault on a compose-internal
// dependency into a NetworkChaos "delay" CRD selecting the dependency's own
// pod by an "app" label (the documented docker-compose->k8s convention),
// since torture.yaml has no pod/namespace concept of its own.
func TestChaosMesh_LatencyOnInternalDependency_ProducesNetworkChaosDelay(t *testing.T) {
	cfg := mustParse(t, platformFixture)
	out, err := ChaosMesh(cfg)
	if err != nil {
		t.Fatalf("ChaosMesh: %v", err)
	}
	for _, want := range []string{"kind: NetworkChaos", "action: delay", "app: postgres", "latency: 300ms", "jitter: 50ms"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

// A bandwidth fault on an external (mock) host has no pod of its own, so it
// must select the SUT's pod and scope the effect via NetworkChaos's
// externalTargets + direction fields — the k8s-native equivalent of pumba's
// --target flag.
func TestChaosMesh_BandwidthOnExternalHost_UsesExternalTargets(t *testing.T) {
	cfg := mustParse(t, platformFixture)
	out, err := ChaosMesh(cfg)
	if err != nil {
		t.Fatalf("ChaosMesh: %v", err)
	}
	for _, want := range []string{"kind: NetworkChaos", "action: bandwidth", "app: checkout-api", "externalTargets", "api.stripe.com", "direction: to"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

// down: true (connection-refused) becomes NetworkChaos's "partition" action,
// cutting traffic in both directions to the dependency's pod.
func TestChaosMesh_DownOnInternalDependency_ProducesPartition(t *testing.T) {
	cfg := mustParse(t, platformFixture)
	out, err := ChaosMesh(cfg)
	if err != nil {
		t.Fatalf("ChaosMesh: %v", err)
	}
	for _, want := range []string{"kind: NetworkChaos", "action: partition", "direction: both", "app: postgres"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

// cpu (with workers) becomes a StressChaos CPU stressor against the SUT's
// own pod.
func TestChaosMesh_CPUOnService_ProducesStressChaos(t *testing.T) {
	cfg := mustParse(t, platformFixture)
	out, err := ChaosMesh(cfg)
	if err != nil {
		t.Fatalf("ChaosMesh: %v", err)
	}
	for _, want := range []string{"kind: StressChaos", "app: checkout-api", "workers: 4", "load: 90"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

// mem/pause have no Chaos Mesh mechanism this package can vouch for (no
// verified SIGSTOP-equivalent PodChaos action; StressChaos's memory
// stressor wants a byte-size string, not the "80%" this fault carries) —
// both MUST be reported skipped, never guessed at (mirrors R-COV-6).
func TestChaosMesh_SkipsUnsupportedDockerActions(t *testing.T) {
	cfg := mustParse(t, platformFixture)
	out, err := ChaosMesh(cfg)
	if err != nil {
		t.Fatalf("ChaosMesh: %v", err)
	}
	if !strings.Contains(out, `fault "mem_squeeze" (inject: mem): not translated by chaosmesh`) {
		t.Errorf("expected mem_squeeze to be reported as skipped, got:\n%s", out)
	}
	if !strings.Contains(out, `fault "api_pause" (inject: pause): not translated by chaosmesh`) {
		t.Errorf("expected api_pause to be reported as skipped, got:\n%s", out)
	}
	if strings.Contains(out, "kind: StressChaos\n  metadata:\n    name: mem-squeeze") {
		t.Errorf("mem_squeeze must not produce a StressChaos document, got:\n%s", out)
	}
}

// A Fault built without going through config.Parse, carrying an
// unrecognized verb, must still surface as an error via fault.Translate —
// ChaosMesh must not silently no-op it.
func TestChaosMesh_UnknownVerbStillErrors(t *testing.T) {
	cfg := &config.Config{
		Target: config.Target{Service: "svc"},
		Faults: []config.Fault{{Name: "bad", Target: "svc", Verb: "not_a_verb", Inject: map[string]any{"not_a_verb": true}}},
	}
	if _, err := ChaosMesh(cfg); err == nil {
		t.Fatal("expected an error for an unrecognized verb")
	}
}

// Every emitted CRD document MUST carry the fields Chaos Mesh's published
// CRD schema requires (apiVersion, kind, metadata.name, spec.selector,
// spec.mode) — checked here by actually parsing the YAML this package
// wrote, per the "verify what you emit" standard. No kubectl or live
// cluster was available in this environment (see the header emitted at the
// top of ChaosMesh's own output), so this is the strongest check available:
// every document is valid YAML and structurally complete, not merely
// string-templated text that looks like YAML.
func TestChaosMesh_EmittedDocumentsHaveRequiredCRDFields(t *testing.T) {
	cfg := mustParse(t, platformFixture)
	out, err := ChaosMesh(cfg)
	if err != nil {
		t.Fatalf("ChaosMesh: %v", err)
	}

	checked := 0
	for _, doc := range strings.Split(out, "\n---\n") {
		if !strings.Contains(doc, "apiVersion: chaos-mesh.org") {
			continue // header text or a skip comment block, not a CRD document
		}
		checked++

		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
			t.Fatalf("emitted document is not valid YAML: %v\ndocument:\n%s", err, doc)
		}

		if parsed["apiVersion"] != "chaos-mesh.org/v1alpha1" {
			t.Errorf("document missing/wrong apiVersion: %v\ndocument:\n%s", parsed["apiVersion"], doc)
		}
		kind, _ := parsed["kind"].(string)
		if kind != "NetworkChaos" && kind != "PodChaos" && kind != "StressChaos" {
			t.Errorf("document has unrecognized kind %q\ndocument:\n%s", kind, doc)
		}

		meta, ok := parsed["metadata"].(map[string]any)
		if !ok || meta["name"] == nil || meta["name"] == "" {
			t.Errorf("document missing metadata.name\ndocument:\n%s", doc)
		}

		spec, ok := parsed["spec"].(map[string]any)
		if !ok {
			t.Fatalf("document missing spec\ndocument:\n%s", doc)
		}
		if spec["mode"] != "all" {
			t.Errorf("document missing spec.mode\ndocument:\n%s", doc)
		}
		selector, ok := spec["selector"].(map[string]any)
		if !ok {
			t.Fatalf("document missing spec.selector\ndocument:\n%s", doc)
		}
		labels, ok := selector["labelSelectors"].(map[string]any)
		if !ok || len(labels) == 0 {
			t.Errorf("document missing spec.selector.labelSelectors\ndocument:\n%s", doc)
		}
		if kind == "NetworkChaos" || kind == "PodChaos" {
			if action, _ := spec["action"].(string); action == "" {
				t.Errorf("%s document missing spec.action\ndocument:\n%s", kind, doc)
			}
		}
	}
	if checked == 0 {
		t.Fatal("expected at least one emitted CRD document to check")
	}
}

// spec: R-CLI-8
// The header MUST state the live-cluster verdict truthfully. Every document
// this emit produces was applied with kubectl to a kind cluster running Chaos
// Mesh 2.8.3 and accepted by its admission webhook (see this file's package
// header for the full record), so the header must name the version it was
// checked against and MUST NOT still carry the superseded "no cluster was
// available" caveat — an unearned claim in either direction is the failure
// R-CLI-8's honesty standard forbids.
func TestChaosMesh_HeaderStatesLiveClusterVerdict(t *testing.T) {
	cfg := mustParse(t, platformFixture)
	out, err := ChaosMesh(cfg)
	if err != nil {
		t.Fatalf("ChaosMesh: %v", err)
	}
	lower := strings.ToLower(out)
	for _, want := range []string{"chaos mesh 2.8.3", "kubectl apply", "accepted"} {
		if !strings.Contains(lower, want) {
			t.Errorf("expected the header to state the live-cluster verdict (%q), got:\n%s", want, out)
		}
	}
	for _, stale := range []string{
		"not validated against a live cluster",
		"no chaos mesh install and no kubectl were available",
		"never applied to or accepted by a\n# real cluster",
	} {
		if strings.Contains(lower, stale) {
			t.Errorf("header still carries the superseded caveat %q, got:\n%s", stale, out)
		}
	}
}

// spec: R-CLI-8
// A fault name longer than Kubernetes' 253-character object-name limit must
// still produce an appliable CRD. Verified against a live cluster: a 254-
// character metadata.name is rejected outright by the API server
// ("metadata.name: Invalid value: ...: must be no more than 253 characters"),
// which would make the emitted document unrunnable — exactly the "tells the
// user to run something that does not work" defect R-CLI-8 forbids.
func TestChaosMesh_LongFaultNameFitsKubernetesNameLimit(t *testing.T) {
	long := strings.Repeat("pg-slow-", 60) // 480 chars, well over the limit
	cfg := &config.Config{
		Target: config.Target{Service: "checkout-api"},
		Faults: []config.Fault{{
			Name:   long,
			Target: "checkout-api",
			Verb:   "latency",
			For:    "10s",
			Inject: map[string]any{"latency": "100ms"},
		}},
	}
	out, err := ChaosMesh(cfg)
	if err != nil {
		t.Fatalf("ChaosMesh: %v", err)
	}
	var found bool
	for _, doc := range strings.Split(out, "\n---\n") {
		if !strings.Contains(doc, "apiVersion: chaos-mesh.org") {
			continue
		}
		var parsed struct {
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &parsed); err != nil {
			t.Fatalf("emitted document is not valid YAML: %v", err)
		}
		found = true
		if n := len(parsed.Metadata.Name); n > 253 {
			t.Errorf("metadata.name is %d characters; the API server rejects anything over 253", n)
		}
		if strings.HasSuffix(parsed.Metadata.Name, "-") || strings.HasPrefix(parsed.Metadata.Name, "-") {
			t.Errorf("metadata.name %q starts or ends with '-'; not a valid RFC1123 name", parsed.Metadata.Name)
		}
	}
	if !found {
		t.Fatal("expected an emitted CRD document to check")
	}
}

// spec: R-CLI-8
// PodChaos's pod-kill is one-shot: it kills the pod once, so torture.yaml's
// "for:" has nothing to bound and the emitted CRD carries no spec.duration.
// Dropping a duration the user wrote without saying so is exactly the silent
// narrowing this package forbids, so the document must say it in words.
func TestChaosMesh_PodKillDisclosesDurationNotCarried(t *testing.T) {
	cfg := &config.Config{
		Target: config.Target{Service: "checkout-api"},
		Faults: []config.Fault{{
			Name:   "api_kill",
			Target: "checkout-api",
			Verb:   "kill",
			For:    "10s",
			Inject: map[string]any{"kill": true},
		}},
	}
	out, err := ChaosMesh(cfg)
	if err != nil {
		t.Fatalf("ChaosMesh: %v", err)
	}
	if !strings.Contains(out, "kind: PodChaos") {
		t.Fatalf("expected a PodChaos document, got:\n%s", out)
	}
	if !strings.Contains(out, "for: 10s") || !strings.Contains(out, "one-shot") {
		t.Errorf("expected the PodChaos document to disclose that for: 10s is not carried (pod-kill is one-shot), got:\n%s", out)
	}
	if strings.Contains(out, "duration:") {
		t.Errorf("PodChaos pod-kill must not carry a spec.duration, got:\n%s", out)
	}
}
