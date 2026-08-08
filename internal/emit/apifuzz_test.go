package emit

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
)

// spec: R-CLI-8

const restlerFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://localhost:8080
  openapi: ./openapi.yaml
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

func restlerSystem() *detect.System {
	return &detect.System{SUT: "checkout-api", Coverage: detect.Coverage{OpenAPI: true}}
}

// spec: R-CLI-8 — "detection never ran" and "this repo has no OpenAPI
// document" are different facts and must not render as the same sentence.
func TestRESTler_DistinguishesNoDetectionFromNoSpec(t *testing.T) {
	cfg := mustParse(t, restlerFixture)

	noDetect, err := RESTler(cfg, nil)
	if err != nil {
		t.Fatalf("RESTler: %v", err)
	}
	if !strings.Contains(noDetect, "could not be detected") {
		t.Errorf("a nil *detect.System must say detection did not run, got:\n%s", noDetect)
	}

	noSpec, err := RESTler(cfg, &detect.System{SUT: "checkout-api"})
	if err != nil {
		t.Fatalf("RESTler: %v", err)
	}
	if !strings.Contains(noSpec, "spec:openapi") {
		t.Errorf("a system without spec:openapi must say so, got:\n%s", noSpec)
	}
	if noSpec == noDetect {
		t.Error("undetected and no-spec produced the same output")
	}
	if !NeedsSystem("restler") {
		t.Error("restler must be registered as needing detection: registry.yaml says when: spec:openapi")
	}
}

// spec: R-CLI-8 — the spec path is torture.yaml's target.openapi and nothing
// else. R-EXE-27 already refuses to guess it by scanning for conventional
// filenames for --fuzz; a second, weaker rule here would be the same guess
// wearing a different hat.
func TestRESTler_RefusesToGuessTheSpecPath(t *testing.T) {
	cfg := mustParse(t, strings.Replace(restlerFixture, "  openapi: ./openapi.yaml\n", "", 1))
	out, err := RESTler(cfg, restlerSystem())
	if err != nil {
		t.Fatalf("RESTler: %v", err)
	}
	if !strings.Contains(out, "target.openapi") {
		t.Errorf("expected a refusal naming target.openapi, got:\n%s", out)
	}
	for _, guess := range []string{"openapi.json", "swagger.json", "openapi.yaml"} {
		if strings.Contains(out, guess) {
			t.Errorf("emitted a guessed spec filename %q:\n%s", guess, out)
		}
	}
}

// spec: R-CLI-8 — RESTler is pointed at a host/port derived from
// target.base_url, never a default. Without one there is nothing to fuzz.
func TestRESTler_RefusesWithoutBaseURL(t *testing.T) {
	cfg := mustParse(t, strings.Replace(restlerFixture, "  base_url: http://localhost:8080\n", "", 1))
	out, err := RESTler(cfg, restlerSystem())
	if err != nil {
		t.Fatalf("RESTler: %v", err)
	}
	if !strings.Contains(out, "base_url") {
		t.Errorf("expected a refusal naming target.base_url, got:\n%s", out)
	}
	if strings.Contains(out, "restler compile") {
		t.Errorf("emitted a runnable fuzz with no target to fuzz:\n%s", out)
	}
}

// spec: R-CLI-8 — the compile config and the engine settings must be the
// real documents RESTler parses: PascalCase compiler keys, snake_case engine
// keys, and no key either of them does not have.
func TestRESTler_EmitsRealConfigDocuments(t *testing.T) {
	out, err := RESTler(mustParse(t, restlerFixture), restlerSystem())
	if err != nil {
		t.Fatalf("RESTler: %v", err)
	}

	compile := restlerExtractJSON(t, out, "RESTLER_COMPILE_CONFIG")
	specs, ok := compile["SwaggerSpecFilePath"].([]any)
	if !ok || len(specs) != 1 {
		t.Fatalf("SwaggerSpecFilePath must be a one-element list of spec paths, got %#v", compile["SwaggerSpecFilePath"])
	}
	// ApiSpecs is a plausible-looking key RESTler does not have.
	if _, bad := compile["ApiSpecs"]; bad {
		t.Error("emitted ApiSpecs, which is not a key RESTler's compiler config parses")
	}

	engine := restlerExtractJSON(t, out, "RESTLER_ENGINE_SETTINGS")
	if engine["host"] != "localhost" {
		t.Errorf("engine host must come from target.base_url, got %#v", engine["host"])
	}
	if port, _ := engine["target_port"].(float64); port != 8080 {
		t.Errorf("engine target_port must come from target.base_url, got %#v", engine["target_port"])
	}
	if engine["no_ssl"] != true {
		t.Errorf("an http:// base_url must set no_ssl true, got %#v", engine["no_ssl"])
	}
	for _, invented := range []string{"time_budget", "max_combinations", "checkers"} {
		if _, bad := engine[invented]; bad {
			t.Errorf("engine settings carry %q, a policy torture.yaml does not state", invented)
		}
	}
}

// spec: R-CLI-8 — an https:// base_url must not be fuzzed over plaintext.
func TestRESTler_HTTPSKeepsTLS(t *testing.T) {
	cfg := mustParse(t, strings.Replace(restlerFixture,
		"base_url: http://localhost:8080", "base_url: https://api.internal.example.com", 1))
	out, err := RESTler(cfg, restlerSystem())
	if err != nil {
		t.Fatalf("RESTler: %v", err)
	}
	engine := restlerExtractJSON(t, out, "RESTLER_ENGINE_SETTINGS")
	if _, present := engine["no_ssl"]; present {
		t.Errorf("no_ssl must be absent for an https base_url, got %#v", engine["no_ssl"])
	}
	if port, present := engine["target_port"]; present {
		t.Errorf("an https base_url with no explicit port must not invent one, got %#v", port)
	}
}

// spec: R-CLI-8 — every fault is reported as untranslated, and the emitted
// script says out loud that RESTler writes to the system it fuzzes.
func TestRESTler_ReportsFaultsAndStatesItMutates(t *testing.T) {
	out, err := RESTler(mustParse(t, restlerFixture), restlerSystem())
	if err != nil {
		t.Fatalf("RESTler: %v", err)
	}
	if !strings.Contains(out, "pg_slow") || !strings.Contains(out, "not translated") {
		t.Errorf("fault pg_slow not reported as untranslated:\n%s", out)
	}
	if !strings.Contains(strings.ToUpper(out), "DELETE") {
		t.Errorf("the emitted script must state that RESTler issues real write requests:\n%s", out)
	}
}

// restlerExtractJSON pulls one heredoc'd JSON document out of the emitted
// script by its delimiter and parses it, so the tests assert against what
// RESTler would actually read rather than against a substring.
func restlerExtractJSON(t *testing.T, script, delim string) map[string]any {
	t.Helper()
	_, after, ok := strings.Cut(script, "<<'"+delim+"'\n")
	if !ok {
		t.Fatalf("no %s heredoc in the emitted script:\n%s", delim, script)
	}
	body, _, ok := strings.Cut(after, "\n"+delim+"\n")
	if !ok {
		t.Fatalf("unterminated %s heredoc in the emitted script", delim)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("%s is not valid JSON: %v\n%s", delim, err, body)
	}
	return doc
}
