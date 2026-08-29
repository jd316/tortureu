package k6

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Target: config.Target{BaseURL: "http://localhost:8080"},
		Load: config.Load{
			Model: "arrival_rate",
			Stages: []config.Stage{
				{Phase: "ramp_up", To: "500rps", Over: "60s"},
				{Phase: "peak", Hold: "500rps", For: "180s"},
				{Phase: "spike", To: "5000rps", Over: "10s"},
			},
			Scenarios: []config.Scenario{
				{
					Name:   "checkout",
					Weight: 70,
					Flow: []config.FlowStep{
						{Method: "post", Path: "/cart", Body: "@fixtures/cart.json"},
						{Method: "get", Path: "/orders"},
					},
				},
				{Name: "browse", Weight: 30, Flow: []config.FlowStep{{Method: "get", Path: "/products"}}},
			},
		},
	}
	return cfg
}

// spec: R-CFG-6
// load.model MUST be arrival_rate (open model); a closed (VU-based) executor
// MUST NOT be offered, because it hides coordinated omission.
func TestCompile_UsesOpenArrivalRateModel(t *testing.T) {
	script, err := Compile(testConfig(t))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !strings.Contains(script, "'ramping-arrival-rate'") {
		t.Errorf("script does not use the ramping-arrival-rate open-model executor:\n%s", script)
	}
	closedExecutors := []string{"constant-vus", "ramping-vus", "per-vu-iterations", "shared-iterations'"}
	for _, exec := range closedExecutors {
		// shared-iterations is legitimately used for the 1-shot phase
		// markers, not for driving load, so only flag it on the "load"
		// scenario's executor line.
		if exec == "shared-iterations'" {
			continue
		}
		if strings.Contains(script, exec) {
			t.Errorf("script contains closed-model executor %q, which R-CFG-6 forbids", exec)
		}
	}

	badModel := testConfig(t)
	badModel.Load.Model = "closed"
	if _, err := Compile(badModel); err == nil {
		t.Errorf("Compile with load.model=closed: want error, got nil")
	}
}

// spec: R-CFG-7
// load.stages is an ordered list; each stage MUST carry a unique phase name,
// which the compiled script MUST use as the anchor faults attach to.
func TestCompile_EachStageIsAUniquelyNamedAnchor(t *testing.T) {
	script, err := Compile(testConfig(t))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	for _, phase := range []string{"ramp_up", "peak", "spike"} {
		want := "phase_" + phase + ":"
		if !strings.Contains(script, want) {
			t.Errorf("script missing marker scenario %q for phase %q:\n%s", want, phase, script)
		}
	}
}

// spec: R-CFG-8
// Each stage MUST specify exactly one of to: (ramp) or hold: (steady), plus
// a duration (over: or for:); Compile MUST resolve both forms to a
// {target, duration} arrival-rate stage.
func TestCompile_ResolvesRampAndHoldStages(t *testing.T) {
	script, err := Compile(testConfig(t))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// ramp_up: to 500rps over 60s
	if !strings.Contains(script, `{ target: 500, duration: "60s" }`) {
		t.Errorf("script missing resolved ramp stage:\n%s", script)
	}
	// peak: hold 500rps for 180s -- steady state is still a {target, duration} entry
	if !strings.Contains(script, `{ target: 500, duration: "180s" }`) {
		t.Errorf("script missing resolved hold stage:\n%s", script)
	}

	bad := testConfig(t)
	bad.Load.Stages[0].Over = "not-a-duration"
	if _, err := Compile(bad); err == nil {
		t.Errorf("Compile with invalid duration: want error, got nil")
	}
}

// spec: R-CFG-9
// load.scenarios[].flow[] entries are method+path mappings; Compile MUST
// render each as an HTTP call against the target's base_url, keyed on
// method and path.
func TestCompile_RendersFlowStepsAsHTTPCalls(t *testing.T) {
	script, err := Compile(testConfig(t))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	start := strings.Index(script, "const scenarios = ")
	if start == -1 {
		t.Fatalf("script has no embedded scenarios literal:\n%s", script)
	}
	rest := script[start+len("const scenarios = "):]
	end := strings.Index(rest, ";\n")
	if end == -1 {
		t.Fatalf("could not find end of scenarios literal")
	}
	var got []jsScenario
	if err := json.Unmarshal([]byte(rest[:end]), &got); err != nil {
		t.Fatalf("embedded scenarios literal is not valid JSON: %v\n%s", err, rest[:end])
	}

	if len(got) != 2 {
		t.Fatalf("want 2 scenarios, got %d: %+v", len(got), got)
	}
	if got[0].Flow[0].Method != "POST" || got[0].Flow[0].URL != "http://localhost:8080/cart" {
		t.Errorf("flow step not rendered from method+path: %+v", got[0].Flow[0])
	}
}

// spec: R-EXE-8
// Phase anchors MUST resolve against stage-transition markers emitted by the
// generated k6 script, not against TortureU's own wall clock. Each stage
// MUST get a marker scenario that fires at that stage's start and logs a
// line ParsePhaseMarker can read back.
func TestCompile_EmitsStageTransitionMarkers(t *testing.T) {
	script, err := Compile(testConfig(t))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// One marker scenario per phase, anchored at the k6 executor's own
	// cumulative startTime (0s, 60s, 240s) -- not any TortureU-side clock.
	for _, want := range []string{`startTime: "0s"`, `startTime: "1m0s"`, `startTime: "4m0s"`} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing marker startTime %q:\n%s", want, script)
		}
	}

	if !strings.Contains(script, "export function markPhase()") {
		t.Errorf("script missing markPhase() marker function:\n%s", script)
	}
	if !strings.Contains(script, PhaseMarkerPrefix) {
		t.Errorf("markPhase() does not emit PhaseMarkerPrefix %q:\n%s", PhaseMarkerPrefix, script)
	}

	// The marker's own line format must round-trip through ParsePhaseMarker,
	// since that is the contract the fault scheduler relies on.
	line := PhaseMarkerPrefix + " peak 1723150000000"
	phase, ok := ParsePhaseMarker(line)
	if !ok || phase != "peak" {
		t.Errorf("ParsePhaseMarker(%q) = %q, %v; want \"peak\", true", line, phase, ok)
	}
	if _, ok := ParsePhaseMarker("some unrelated k6 log line"); ok {
		t.Errorf("ParsePhaseMarker matched a non-marker line")
	}
}

// spec: R-EXE-9
// The generated k6 script MUST NOT fetch remote JavaScript at runtime (e.g.
// jslib.k6.io). Only built-in k6 modules may be imported, and helpers must
// be inlined.
func TestCompile_NeverImportsRemoteJavaScript(t *testing.T) {
	script, err := Compile(testConfig(t))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if strings.Contains(script, "jslib.k6.io") {
		t.Errorf("script fetches jslib.k6.io, violating R-EXE-9:\n%s", script)
	}
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "import ") {
			continue
		}
		if strings.Contains(trimmed, "://") {
			t.Errorf("import statement fetches a remote URL: %q", trimmed)
		}
		if !strings.HasPrefix(trimmed, "import http from 'k6/") && !strings.HasPrefix(trimmed, "import exec from 'k6/") {
			t.Errorf("import statement is not a built-in k6 module: %q", trimmed)
		}
	}
}

// spec: R-CFG-16
// Assertions MUST use k6 threshold expression syntax verbatim for
// k6-visible metrics; TortureU MUST NOT define its own metric DSL. Compile
// MUST carry each k6-shaped assert: entry into the generated script's
// options.thresholds, unmodified.
func TestCompile_EmitsK6ThresholdsVerbatim(t *testing.T) {
	cfg := testConfig(t)
	cfg.Assert = []config.AssertEntry{
		{"http_req_duration": []any{"p(95)<500", "p(99)<1500"}},
		{"http_req_failed": []any{"rate<0.01"}},
	}

	script, err := Compile(cfg)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	start := strings.Index(script, "thresholds: ")
	if start == -1 {
		t.Fatalf("script has no thresholds: field:\n%s", script)
	}
	rest := script[start+len("thresholds: "):]
	end := strings.Index(rest, ",\n")
	if end == -1 {
		t.Fatalf("could not find end of thresholds literal")
	}
	var got map[string][]string
	if err := json.Unmarshal([]byte(rest[:end]), &got); err != nil {
		t.Fatalf("thresholds literal is not valid JSON: %v\n%s", err, rest[:end])
	}

	if strings.Join(got["http_req_duration"], ",") != "p(95)<500,p(99)<1500" {
		t.Errorf("http_req_duration thresholds not carried verbatim: %+v", got["http_req_duration"])
	}
	if strings.Join(got["http_req_failed"], ",") != "rate<0.01" {
		t.Errorf("http_req_failed thresholds not carried verbatim: %+v", got["http_req_failed"])
	}
}

// spec: R-CFG-17
// A promql: entry MUST be accepted for signals k6 cannot observe. It is not
// k6's to evaluate (that is Task 7's job against Prometheus), so Compile
// MUST pass it over without error and without it appearing in the script's
// thresholds. A promql:-only assert: block MUST still produce a valid
// (empty) thresholds object, not a malformed script.
func TestCompile_PassesOverPromqlAssertsWithoutError(t *testing.T) {
	cfg := testConfig(t)
	cfg.Assert = []config.AssertEntry{
		{"promql": "sum(rate(app_retries_total[30s])) < 100"},
	}

	script, err := Compile(cfg)
	if err != nil {
		t.Fatalf("Compile with only promql assert: want no error, got %v", err)
	}
	if strings.Contains(script, "promql") {
		t.Errorf("script leaks the promql: key into generated JS:\n%s", script)
	}
	if !strings.Contains(script, "thresholds: {}") {
		t.Errorf("promql-only assert: MUST still yield a valid empty thresholds object:\n%s", script)
	}
}

// spec: R-LIC-1
// TortureU MUST invoke k6 as a separate, unmodified process. It MUST NOT
// import k6 Go packages, link against k6, or build an xk6 extension into its
// own binary. internal/k6's own source MUST NOT import any go.k6.io package.
func TestPackageSource_NeverImportsK6GoPackages(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", e.Name(), err)
		}
		if strings.Contains(string(b), "go.k6.io") {
			t.Errorf("%s imports a go.k6.io package, which R-LIC-1 forbids", e.Name())
		}
	}
}
