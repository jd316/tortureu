package emit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/detect"
)

// obsFixture exercises all three observability emitters at once: it has
// promql: asserts (the only queries a dashboard may honestly plot, since
// they are the user's own), k6-threshold asserts (which no Prometheus
// scrapes unless the user configures k6's remote-write output), a fault
// window (which pyroscope's baseline-vs-fault comparison is anchored to),
// and a SUT service name (which jaeger uses for OTEL_SERVICE_NAME).
const obsFixture = `
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
    - phase: warm
      hold: 50rps
      for: 30s
    - phase: peak
      hold: 500rps
      for: 60s
faults:
  - name: stripe_slow
    at: peak
    for: 40s
    target: api.stripe.com
    inject: { latency: 2s }
  - name: pg_down
    at: peak+10s
    for: 10s
    target: postgres:5432
    inject: { down: true }
assert:
  - http_req_duration: ["p(95)<500"]
  - promql: 'sum(rate(app_retries_total[30s])) < 100'
  - promql: 'pg_stat_activity_count / pg_settings_max_connections < 0.9'
`

// ---------------------------------------------------------------- dashboard

// spec: R-CLI-8 — `emit dashboard` must produce a runnable artefact for a
// delegate-tier tool. For Grafana "runnable" means a document its import
// API accepts, so the first thing asserted is that it is valid JSON with
// the fields Grafana's importer requires.
func TestDashboard_IsValidGrafanaDashboardJSON(t *testing.T) {
	cfg := mustParse(t, obsFixture)
	out, err := Dashboard(cfg, nil)
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	var dash map[string]any
	if err := json.Unmarshal([]byte(out), &dash); err != nil {
		t.Fatalf("emitted dashboard is not valid JSON: %v\n%s", err, out)
	}
	for _, key := range []string{"title", "schemaVersion", "panels", "templating", "time"} {
		if _, ok := dash[key]; !ok {
			t.Errorf("dashboard is missing required key %q", key)
		}
	}
	if title, _ := dash["title"].(string); !strings.Contains(title, "checkout-api") {
		t.Errorf("expected the SUT service in the title, got %q", title)
	}
}

// spec: R-CLI-8 — the emitted config must come from torture.yaml. Every
// panel query is a promql: assert the user wrote, verbatim: a panel whose
// query TortureU invented would plot a metric nothing exports.
func TestDashboard_PanelsPlotOnlyUserWrittenPromqlAsserts(t *testing.T) {
	cfg := mustParse(t, obsFixture)
	out, err := Dashboard(cfg, nil)
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	exprs := obsTestPanelExprs(t, out)
	want := []string{
		"sum(rate(app_retries_total[30s])) < 100",
		"pg_stat_activity_count / pg_settings_max_connections < 0.9",
	}
	if len(exprs) != len(want) {
		t.Fatalf("expected exactly %d query panels (one per promql: assert), got %d: %v", len(want), len(exprs), exprs)
	}
	for i, w := range want {
		if exprs[i] != w {
			t.Errorf("panel %d expr = %q, want the assert verbatim %q", i, exprs[i], w)
		}
	}
}

// spec: R-CLI-8 — a fault this tool does not translate must be reported,
// never dropped. A k6-threshold assert is not plottable here (nothing
// scrapes k6 unless the user wires its remote-write output), so it must be
// named in the dashboard's own disclosure panel rather than silently
// omitted.
func TestDashboard_ReportsK6AssertsItCannotPlot(t *testing.T) {
	cfg := mustParse(t, obsFixture)
	out, err := Dashboard(cfg, nil)
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	notes := obsTestTextPanels(t, out)
	if !strings.Contains(notes, "http_req_duration") {
		t.Errorf("expected the un-plottable k6 assert to be named in the disclosure panel, got:\n%s", notes)
	}
	if !strings.Contains(notes, "remote-write") {
		t.Errorf("expected the disclosure panel to say why k6 metrics are absent, got:\n%s", notes)
	}
}

// spec: R-CLI-8 — the dashboard must not invent a datasource UID. It
// binds every panel to a dashboard datasource variable, which Grafana
// prompts for at import time, so the artefact never claims to know which
// Prometheus the user runs.
func TestDashboard_UsesADatasourceVariableRatherThanAGuessedUID(t *testing.T) {
	cfg := mustParse(t, obsFixture)
	out, err := Dashboard(cfg, nil)
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	var dash struct {
		Templating struct {
			List []struct {
				Name  string `json:"name"`
				Type  string `json:"type"`
				Query string `json:"query"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal([]byte(out), &dash); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(dash.Templating.List) != 1 {
		t.Fatalf("expected exactly one template variable, got %d", len(dash.Templating.List))
	}
	v := dash.Templating.List[0]
	if v.Type != "datasource" || v.Query != "prometheus" {
		t.Errorf("expected a prometheus datasource variable, got %+v", v)
	}
	for _, uid := range obsTestDatasourceUIDs(t, out) {
		if uid != "${"+v.Name+"}" {
			t.Errorf("panel binds a literal datasource uid %q instead of the ${%s} variable", uid, v.Name)
		}
	}
}

// spec: R-COV-6 — a fact detection could not establish must be reported as
// unevaluable, never silently treated as false. Whether a Prometheus exists
// at all is such a fact: with no detection result the dashboard must say
// detection did not run, not assert either way.
func TestDashboard_DisclosesWhetherAPrometheusWasDetected(t *testing.T) {
	cfg := mustParse(t, obsFixture)

	out, err := Dashboard(cfg, nil)
	if err != nil {
		t.Fatalf("Dashboard(nil sys): %v", err)
	}
	if notes := obsTestTextPanels(t, out); !strings.Contains(notes, "detection did not run") {
		t.Errorf("expected a nil *detect.System to be disclosed, got:\n%s", notes)
	}

	none := &detect.System{}
	out, err = Dashboard(cfg, none)
	if err != nil {
		t.Fatalf("Dashboard(no metrics backend): %v", err)
	}
	if notes := obsTestTextPanels(t, out); !strings.Contains(notes, "found no metrics backend") {
		t.Errorf("expected an absent metrics backend to be disclosed, got:\n%s", notes)
	}

	found := &detect.System{Obs: detect.Obs{Metrics: true}}
	out, err = Dashboard(cfg, found)
	if err != nil {
		t.Fatalf("Dashboard(metrics backend): %v", err)
	}
	if notes := obsTestTextPanels(t, out); !strings.Contains(notes, "A metrics backend was detected") {
		t.Errorf("expected a detected metrics backend to be disclosed, got:\n%s", notes)
	}
}

// spec: R-CLI-8 — with no promql: assert there is nothing honest to plot.
// The emitter must still produce an importable dashboard that says so,
// rather than filling it with metric names the system may not export.
func TestDashboard_NoPromqlAsserts_EmitsNoInventedPanels(t *testing.T) {
	cfg := &config.Config{
		Target: config.Target{Service: "svc"},
		Assert: []config.AssertEntry{{"http_req_failed": []any{"rate<0.01"}}},
	}
	out, err := Dashboard(cfg, nil)
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	if exprs := obsTestPanelExprs(t, out); len(exprs) != 0 {
		t.Errorf("expected no query panels without promql: asserts, got %v", exprs)
	}
	if notes := obsTestTextPanels(t, out); !strings.Contains(notes, "no promql:") {
		t.Errorf("expected the empty case to be explained in the dashboard, got:\n%s", notes)
	}
}

// ---------------------------------------------------------------- pyroscope

// spec: R-CLI-8 — `emit pyroscope` must generate a runnable command. The
// docker run line is the runnable part; the profiling of the SUT itself is
// not something this tool can do for the user, so what it cannot do must be
// stated rather than implied.
func TestPyroscope_EmitsRunnableServerCommand(t *testing.T) {
	cfg := mustParse(t, obsFixture)
	out, err := Pyroscope(cfg, nil)
	if err != nil {
		t.Fatalf("Pyroscope: %v", err)
	}
	if !strings.Contains(out, "docker run") || !strings.Contains(out, "grafana/pyroscope") {
		t.Errorf("expected a docker run command for the pyroscope server, got:\n%s", out)
	}
	if !strings.Contains(out, "4040") {
		t.Errorf("expected the pyroscope ingest/UI port, got:\n%s", out)
	}
}

// spec: R-CLI-8 — the emitted config must be derived from torture.yaml.
// The whole value of profiling during a torture run is comparing the fault
// window against the phase before it, so each fault's at:/for: window must
// appear in the render queries the script issues.
func TestPyroscope_ComparisonWindowsComeFromTheFaults(t *testing.T) {
	cfg := mustParse(t, obsFixture)
	out, err := Pyroscope(cfg, nil)
	if err != nil {
		t.Fatalf("Pyroscope: %v", err)
	}
	for _, name := range []string{"stripe_slow", "pg_down"} {
		if !strings.Contains(out, name) {
			t.Errorf("expected fault %q to get a comparison block, got:\n%s", name, out)
		}
	}
	if !strings.Contains(out, "/pyroscope/render") {
		t.Errorf("expected the verified render API path, got:\n%s", out)
	}
	if !strings.Contains(out, "40s") {
		t.Errorf("expected the fault's for: window to size the comparison, got:\n%s", out)
	}
}

// spec: R-CLI-8 — an instrumentation hint may only be given for a language
// detection actually established. An undetected language must produce the
// full list with a note, never a guess at one.
func TestPyroscope_LanguageHintOnlyWhenDetected(t *testing.T) {
	cfg := mustParse(t, obsFixture)

	out, err := Pyroscope(cfg, &detect.System{Lang: "go"})
	if err != nil {
		t.Fatalf("Pyroscope: %v", err)
	}
	if !strings.Contains(out, "github.com/grafana/pyroscope-go") {
		t.Errorf("expected the Go SDK for a detected Go SUT, got:\n%s", out)
	}

	out, err = Pyroscope(cfg, nil)
	if err != nil {
		t.Fatalf("Pyroscope: %v", err)
	}
	if !strings.Contains(out, "language was not detected") {
		t.Errorf("expected an undetected language to be disclosed, got:\n%s", out)
	}
}

// ------------------------------------------------------------------- jaeger

// spec: R-CLI-8 — `emit jaeger` must generate a runnable command: the
// collector container, plus the OTLP environment the SUT needs to reach it.
func TestJaeger_EmitsRunnableCollectorAndSUTEnvironment(t *testing.T) {
	cfg := mustParse(t, obsFixture)
	out, err := Jaeger(cfg, nil)
	if err != nil {
		t.Fatalf("Jaeger: %v", err)
	}
	if !strings.Contains(out, "docker run") || !strings.Contains(out, "jaegertracing/jaeger") {
		t.Errorf("expected a docker run command for jaeger, got:\n%s", out)
	}
	for _, want := range []string{"4317", "4318", "16686", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_SERVICE_NAME=checkout-api"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in the emitted jaeger setup, got:\n%s", want, out)
		}
	}
}

// spec: R-COV-6 — jaeger's registry predicate is `lacks:otel`, which
// detection computes as a tri-state. Each of the three states, plus "no
// detection ran", must be reported distinctly: collapsing unknown into
// "applies" would recommend instrumenting a system that may already be
// instrumented.
func TestJaeger_ReportsTheLacksOtelTriStateWithoutCollapsingUnknown(t *testing.T) {
	cfg := mustParse(t, obsFixture)
	cases := []struct {
		name    string
		sys     *detect.System
		want    string
		notWant string
	}{
		{"true", &detect.System{Coverage: detect.Coverage{LacksOtel: detect.FactTrue}}, "lacks:otel = true", ""},
		{"false", &detect.System{Coverage: detect.Coverage{LacksOtel: detect.FactFalse}}, "lacks:otel = false", "lacks:otel = true"},
		{"unknown", &detect.System{Coverage: detect.Coverage{LacksOtel: detect.FactUnknown}}, "lacks:otel = unknown", "lacks:otel = true"},
		{"no detection", nil, "detection did not run", "lacks:otel = true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Jaeger(cfg, tc.sys)
			if err != nil {
				t.Fatalf("Jaeger: %v", err)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("expected %q in the emitted header, got:\n%s", tc.want, out)
			}
			if tc.notWant != "" && strings.Contains(out, tc.notWant) {
				t.Errorf("emitted header must not claim %q for the %s case:\n%s", tc.notWant, tc.name, out)
			}
		})
	}
}

// spec: R-COV-6 — when OTel is already present the honest advice is to
// point the existing pipeline at Jaeger, not to instrument from scratch.
// The emitter must say that instead of quietly emitting the same script.
func TestJaeger_OtelAlreadyPresent_RecommendsReusingTheExistingPipeline(t *testing.T) {
	cfg := mustParse(t, obsFixture)
	out, err := Jaeger(cfg, &detect.System{Coverage: detect.Coverage{LacksOtel: detect.FactFalse}})
	if err != nil {
		t.Fatalf("Jaeger: %v", err)
	}
	if !strings.Contains(out, "already") || !strings.Contains(out, "collector") {
		t.Errorf("expected advice to reuse the existing OTel pipeline, got:\n%s", out)
	}
}

// spec: R-CLI-8 — every emitter in this package must be reachable through
// the registry under the name registry.yaml's how: field advertises
// (grafana is reached as `tortureu emit dashboard`).
func TestObservabilityEmittersAreRegistered(t *testing.T) {
	for _, tool := range []string{"dashboard", "pyroscope", "jaeger"} {
		if _, err := Emit(tool, mustParse(t, obsFixture), nil); err != nil {
			t.Errorf("Emit(%q): %v", tool, err)
		}
		if !NeedsSystem(tool) {
			t.Errorf("Emit(%q) consults *detect.System and must declare needsSystem", tool)
		}
	}
}

// ------------------------------------------------------------------ helpers

// obsTestPanelExprs returns the PromQL expression of every query panel, in
// panel order.
func obsTestPanelExprs(t *testing.T, out string) []string {
	t.Helper()
	var dash struct {
		Panels []struct {
			Type    string `json:"type"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal([]byte(out), &dash); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	var exprs []string
	for _, p := range dash.Panels {
		for _, tg := range p.Targets {
			exprs = append(exprs, tg.Expr)
		}
	}
	return exprs
}

// obsTestTextPanels returns the markdown of every text panel concatenated —
// the dashboard's own disclosure surface, since JSON cannot carry comments.
func obsTestTextPanels(t *testing.T, out string) string {
	t.Helper()
	var dash struct {
		Panels []struct {
			Type    string `json:"type"`
			Options struct {
				Content string `json:"content"`
			} `json:"options"`
		} `json:"panels"`
	}
	if err := json.Unmarshal([]byte(out), &dash); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	var b strings.Builder
	for _, p := range dash.Panels {
		if p.Type == "text" {
			b.WriteString(p.Options.Content)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// obsTestDatasourceUIDs returns every datasource uid referenced anywhere in
// the dashboard, so a hardcoded one cannot slip in unnoticed.
func obsTestDatasourceUIDs(t *testing.T, out string) []string {
	t.Helper()
	var raw any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var uids []string
	var walk func(v any)
	walk = func(v any) {
		switch n := v.(type) {
		case map[string]any:
			for k, child := range n {
				if k == "datasource" {
					if ds, ok := child.(map[string]any); ok {
						if uid, ok := ds["uid"].(string); ok {
							uids = append(uids, uid)
						}
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range n {
				walk(child)
			}
		}
	}
	walk(raw)
	return uids
}

// ---------------------------------------------------------- live verification

// spec: R-CLI-8 — "generates a runnable command or config" is a claim about
// a real tool accepting the output, and this is the test that actually
// makes Grafana accept it: the emitted JSON is POSTed to a live Grafana's
// dashboard import API and the created dashboard is read back. It is opt-in
// (TORTUREU_EMIT_LIVE=1 plus a Grafana at TORTUREU_GRAFANA_URL) because the
// default `go test ./...` gate must not need a container — but it is what
// the file header's VERIFIED claim rests on, so the claim can be re-checked
// rather than believed:
//
//	docker run -d --name tu-graf -p 3000:3000 grafana/grafana:latest
//	TORTUREU_EMIT_LIVE=1 go test ./internal/emit/ -run LiveGrafana -v
func TestDashboard_AcceptedByLiveGrafanaImportAPI(t *testing.T) {
	if os.Getenv("TORTUREU_EMIT_LIVE") != "1" {
		t.Skip("set TORTUREU_EMIT_LIVE=1 (and run a Grafana) to verify against a live import API")
	}
	base := os.Getenv("TORTUREU_GRAFANA_URL")
	if base == "" {
		base = "http://admin:admin@localhost:3000"
	}

	out, err := Dashboard(mustParse(t, obsFixture), nil)
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	var dash map[string]any
	if err := json.Unmarshal([]byte(out), &dash); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	body, err := json.Marshal(map[string]any{"dashboard": dash, "overwrite": true, "message": "tortureu emit dashboard"})
	if err != nil {
		t.Fatalf("marshal import payload: %v", err)
	}

	resp, err := http.Post(base+"/api/dashboards/db", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST to Grafana: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Grafana rejected the dashboard: %s\n%s", resp.Status, got)
	}
	var created struct {
		Status string `json:"status"`
		UID    string `json:"uid"`
	}
	if err := json.Unmarshal(got, &created); err != nil || created.Status != "success" {
		t.Fatalf("unexpected import response: %s", got)
	}

	// Read it back: an accepted POST proves the payload parsed; fetching the
	// stored dashboard proves Grafana kept the panels rather than dropping
	// them as unrecognized.
	back, err := http.Get(base + "/api/dashboards/uid/" + created.UID)
	if err != nil {
		t.Fatalf("GET stored dashboard: %v", err)
	}
	defer back.Body.Close()
	stored, _ := io.ReadAll(back.Body)
	for _, want := range []string{"app_retries_total", "${" + obsDatasourceVar + "}", "TortureU — checkout-api"} {
		if !strings.Contains(string(stored), want) {
			t.Errorf("stored dashboard is missing %q:\n%s", want, stored)
		}
	}
}

// spec: R-CLI-8 — "runnable" is the claim, and the first version of this
// script was not: run end to end against a real pyroscope container it
// exited 56 (curl could not connect), because the server answers /ready
// only ~20s after `docker run` returns. The emitted script must wait rather
// than hand the user a race.
func TestPyroscope_WaitsForTheServerBeforeQueryingIt(t *testing.T) {
	out, err := Pyroscope(mustParse(t, obsFixture), nil)
	if err != nil {
		t.Fatalf("Pyroscope: %v", err)
	}
	if !strings.Contains(out, "/ready") {
		t.Errorf("expected the emitted script to wait on pyroscope's readiness endpoint, got:\n%s", out)
	}
	ready := strings.Index(out, "/ready")
	query := strings.Index(out, "render-diff")
	if query >= 0 && ready > query {
		t.Errorf("readiness wait must come before the first query, got:\n%s", out)
	}
}
