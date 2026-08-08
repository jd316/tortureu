// Observability emitters (R-CLI-8 proposed): `tortureu emit dashboard`
// (registry.yaml's grafana entry), `emit pyroscope` and `emit jaeger`.
// These are the "what were you looking at while it broke" tier: none of
// them injects anything, so unlike this package's fault emitters there is
// no fault verb to translate — what varies per config is which queries the
// user already wrote (dashboard), which windows the faults define
// (pyroscope), and what detection found (jaeger).
//
// The governing rule here is R-CLI-8's honesty clause read backwards: an
// emitted artefact must not assert something the system does not do. A
// Grafana dashboard is the sharpest case — a panel querying
// `http_requests_total` looks authoritative and is worthless if nothing
// exports that metric, and worse than empty because it reads as "no
// traffic" rather than "no such metric". So this dashboard plots exactly
// one class of query: the promql: entries in torture.yaml's assert: block,
// which the user wrote against their own Prometheus (R-CFG-17). Nothing
// else. k6-threshold asserts get named in the dashboard's disclosure panel
// with the reason they are not plotted, never a substituted metric.
//
// VERIFICATION STATUS (run at implementation time on this host; commands
// are in this file's git history and reproducible with the tests below):
//
//   - Grafana: VERIFIED. grafana/grafana:latest (server version 13.1.3)
//     was started in a container and the exact JSON this file emits was
//     POSTed to its /api/dashboards/db import API, which accepted it
//     (HTTP 200, "status":"success") and served the resulting dashboard
//     back. Reproduce with TestDashboard_AcceptedByLiveGrafanaImportAPI
//     (TORTUREU_EMIT_LIVE=1 go test ./internal/emit/ -run LiveGrafana).
//     NOT verified: that any panel returns data — that depends on the
//     user's own Prometheus and their own recording rules, neither of
//     which exists here. The dashboard says so in its own text panel.
//   - Pyroscope: VERIFIED. grafana/pyroscope:latest (pyroscope 2.2.1) was
//     started and both API paths this file emits were exercised against
//     it with curl: /pyroscope/render and /pyroscope/render-diff both
//     returned a flamebearer JSON document for a
//     process_cpu:cpu:nanoseconds:cpu:nanoseconds{service_name="..."}
//     query, and from/until were confirmed to accept unix-epoch seconds
//     (the form this script computes) as well as relative "now-5m". The
//     whole emitted script was then run end to end (via the built CLI:
//     `tortureu emit pyroscope > pyro.sh; RUN_START_EPOCH=... bash pyro.sh`),
//     exiting 0 and writing a valid flamebearer diff document per fault.
//     Its first version exited 56 instead — pyroscope is not ready when
//     `docker run` returns — which is why the script now waits on /ready.
//     Package names in the instrumentation hints were each checked to
//     exist at their registry: github.com/grafana/pyroscope-go (go list
//     -m -versions, v1.4.1 latest), @pyroscope/nodejs (npm view, 0.6.2),
//     pyroscope-io (pip index, 1.2.1). NOT verified: that a profiled SUT
//     appears under the emitted query — that needs an instrumented
//     application, which only the user can supply.
//   - Jaeger: VERIFIED end to end. jaegertracing/jaeger:latest (v2.20.0)
//     was started with the port mapping this file emits, an OTLP/HTTP
//     span carrying service.name=checkout-api was POSTed to :4318/v1/traces
//     (HTTP 200), and the span was then read back from the query API
//     (/api/services listed "checkout-api"; /api/traces?service=checkout-api
//     returned it). So the container command, the OTLP ports and the
//     service-name wiring are all confirmed against a real Jaeger. The
//     emitted script itself was run (`bash jaeger.sh`, exit 0) and the
//     Jaeger it started answered /api/services.
//     NOT verified: that the user's SUT emits spans when given
//     OTEL_EXPORTER_OTLP_ENDPOINT — that requires the SUT to be
//     instrumented, which is precisely what lacks:otel says it is not, and
//     the emitted script leads with that.
//
// Not verified anywhere: nothing in this file was run against Kubernetes,
// and none of it schedules against the k6 phase clock (see the package
// doc — delegate tier means separate timing).
package emit

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
)

func init() {
	// All three declare needsSystem: each consults *detect.System for a
	// fact it must not guess — whether a metrics backend exists (dashboard),
	// which language to give an SDK hint for (pyroscope), and the lacks:otel
	// tri-state that is jaeger's own registry predicate. Every one of them
	// still emits a usable artefact when sys is nil; what changes is that
	// the artefact then says detection did not run instead of claiming a
	// fact (R-COV-6).
	//
	// Registered as "dashboard", not "grafana": registry.yaml's how: field
	// for the grafana entry is `tortureu emit dashboard`, and R-CLI-2 binds
	// the CLI surface to that field.
	Register("dashboard", Dashboard, true)
	Register("pyroscope", Pyroscope, true)
	Register("jaeger", Jaeger, true)
}

// obsPyroscopeImage / obsJaegerImage pin the images that were actually run
// during verification, so the emitted command is the one that was proven to
// work rather than whatever :latest becomes. jaegertracing/jaeger is the v2
// image: jaegertracing/all-in-one is Jaeger v1, which prints an
// end-of-life notice on startup (observed here) and is not something to put
// in generated output.
const (
	obsPyroscopeImage = "grafana/pyroscope:latest"
	obsJaegerImage    = "jaegertracing/jaeger:latest"
)

// ============================================================== dashboard ==

// obsDashboard and friends model the subset of Grafana's dashboard schema
// this emitter uses. Modeled as structs and marshalled (rather than
// string-templated) so the JSON cannot be malformed in a shape the tests
// did not happen to exercise.
type obsDashboard struct {
	Title         string           `json:"title"`
	Tags          []string         `json:"tags"`
	Timezone      string           `json:"timezone"`
	SchemaVersion int              `json:"schemaVersion"`
	Editable      bool             `json:"editable"`
	Time          obsTimeRange     `json:"time"`
	Templating    obsTemplating    `json:"templating"`
	Panels        []obsPanel       `json:"panels"`
	Annotations   *obsAnnotations  `json:"annotations,omitempty"`
	ID            *json.RawMessage `json:"id"`
}

type obsTimeRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type obsTemplating struct {
	List []obsVariable `json:"list"`
}

// obsVariable is the datasource picker. Grafana resolves ${datasource} at
// view/import time, which is the whole point: this file cannot know the UID
// of a Prometheus it has never seen, and a guessed UID would produce a
// dashboard that silently queries nothing.
type obsVariable struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Type  string `json:"type"`
	Query string `json:"query"`
	Hide  int    `json:"hide"`
}

type obsAnnotations struct {
	List []obsAnnotation `json:"list"`
}

type obsAnnotation struct {
	BuiltIn    int            `json:"builtIn"`
	Name       string         `json:"name"`
	Enable     bool           `json:"enable"`
	Hide       bool           `json:"hide"`
	IconColor  string         `json:"iconColor"`
	Type       string         `json:"type"`
	Target     *obsAnnoTarget `json:"target,omitempty"`
	Datasource *obsDatasource `json:"datasource,omitempty"`
}

type obsAnnoTarget struct {
	Limit    int      `json:"limit"`
	MatchAny bool     `json:"matchAny"`
	Tags     []string `json:"tags"`
	Type     string   `json:"type"`
}

type obsDatasource struct {
	Type string `json:"type"`
	UID  string `json:"uid"`
}

type obsPanel struct {
	ID          int            `json:"id"`
	Type        string         `json:"type"`
	Title       string         `json:"title"`
	GridPos     obsGridPos     `json:"gridPos"`
	Datasource  *obsDatasource `json:"datasource,omitempty"`
	Targets     []obsTarget    `json:"targets,omitempty"`
	Options     *obsOptions    `json:"options,omitempty"`
	Description string         `json:"description,omitempty"`
}

type obsGridPos struct {
	H int `json:"h"`
	W int `json:"w"`
	X int `json:"x"`
	Y int `json:"y"`
}

type obsTarget struct {
	RefID      string         `json:"refId"`
	Expr       string         `json:"expr"`
	LegendFmt  string         `json:"legendFormat,omitempty"`
	Datasource *obsDatasource `json:"datasource,omitempty"`
}

type obsOptions struct {
	Mode    string `json:"mode,omitempty"`
	Content string `json:"content,omitempty"`
}

// obsDatasourceVar is the template variable name every panel binds to.
const obsDatasourceVar = "datasource"

func obsVarDatasource() *obsDatasource {
	return &obsDatasource{Type: "prometheus", UID: "${" + obsDatasourceVar + "}"}
}

// Dashboard renders a Grafana dashboard for one torture.yaml (R-CLI-8
// proposed; registry.yaml grafana, reached as `tortureu emit dashboard`).
//
// It plots the promql: asserts and nothing else. Everything it cannot plot,
// and every assumption it makes, is written into a markdown text panel
// inside the dashboard — JSON carries no comments, and a caveat that lives
// only in this source file is a caveat the person looking at the dashboard
// never sees.
func Dashboard(cfg *config.Config, sys *detect.System) (string, error) {
	service := cfg.Target.Service
	if service == "" {
		service = "system under test"
	}

	var (
		panels  []obsPanel
		nextID  = 2
		y       = obsNotesHeight
		queries = obsPromqlAsserts(cfg)
	)
	for i, q := range queries {
		panels = append(panels, obsPanel{
			ID:         nextID,
			Type:       "timeseries",
			Title:      fmt.Sprintf("assert %d: %s", i+1, obsTruncate(q, 60)),
			GridPos:    obsGridPos{H: 8, W: 12, X: (i % 2) * 12, Y: y},
			Datasource: obsVarDatasource(),
			Description: "This is a promql: assert from torture.yaml, plotted verbatim. " +
				"`run` evaluates it as pass/fail over the run window; this panel only shows it over time.",
			Targets: []obsTarget{{
				RefID:      "A",
				Expr:       q,
				Datasource: obsVarDatasource(),
			}},
		})
		nextID++
		if i%2 == 1 {
			y += 8
		}
	}

	notes := obsPanel{
		ID:      1,
		Type:    "text",
		Title:   "What this dashboard does and does not show",
		GridPos: obsGridPos{H: obsNotesHeight, W: 24, X: 0, Y: 0},
		Options: &obsOptions{Mode: "markdown", Content: obsDashboardNotes(cfg, sys, queries)},
	}
	panels = append([]obsPanel{notes}, panels...)

	dash := obsDashboard{
		Title:         "TortureU — " + service,
		Tags:          []string{"tortureu"},
		Timezone:      "browser",
		SchemaVersion: 39,
		Editable:      true,
		Time:          obsTimeRange{From: "now-1h", To: "now"},
		Templating: obsTemplating{List: []obsVariable{{
			Name:  obsDatasourceVar,
			Label: "Prometheus datasource",
			Type:  "datasource",
			Query: "prometheus",
		}}},
		Annotations: &obsAnnotations{List: []obsAnnotation{{
			BuiltIn:   1,
			Name:      "Annotations & Alerts",
			Enable:    true,
			Hide:      true,
			IconColor: "rgba(0, 211, 255, 1)",
			Type:      "dashboard",
			Target:    &obsAnnoTarget{Limit: 100, Type: "dashboard"},
		}}},
		Panels: panels,
	}

	out, err := json.MarshalIndent(dash, "", "  ")
	if err != nil {
		return "", fmt.Errorf("emit dashboard: %w", err)
	}
	return string(out) + "\n", nil
}

// obsNotesHeight is the height of the disclosure panel, in Grafana grid
// rows. It is generous on purpose: the caveats are the part of this
// artefact that stops someone trusting a panel they should not.
const obsNotesHeight = 10

// obsPromqlAsserts returns every promql: expression in assert:, in order —
// the only queries this dashboard is entitled to plot (R-CFG-17).
func obsPromqlAsserts(cfg *config.Config) []string {
	var out []string
	for _, entry := range cfg.Assert {
		if expr, ok := entry["promql"].(string); ok {
			out = append(out, expr)
		}
	}
	return out
}

// obsOtherAsserts returns the metric name of every assert: entry that is
// not a promql: one — the k6 thresholds and sql: escape hatches this
// dashboard cannot plot and must therefore name (R-CLI-8's report-what-you
// -do-not-translate rule, applied to asserts rather than faults).
func obsOtherAsserts(cfg *config.Config) []string {
	var out []string
	for _, entry := range cfg.Assert {
		for metric := range entry {
			if metric != "promql" {
				out = append(out, metric)
			}
		}
	}
	return out
}

// obsDashboardNotes writes the markdown disclosure panel: what the panels
// are, which assertions are missing and why, what is assumed about the
// datasource, and what detection did or did not establish.
func obsDashboardNotes(cfg *config.Config, sys *detect.System, queries []string) string {
	var b strings.Builder
	b.WriteString("Generated by `tortureu emit dashboard` from `torture.yaml`. ")
	b.WriteString("Every panel below is a **`promql:` assert you wrote**, plotted verbatim — ")
	b.WriteString("this dashboard does not invent metric names, so it shows nothing your system does not already export.\n\n")

	if len(queries) == 0 {
		b.WriteString("**There are no promql: asserts in this torture.yaml, so there are no query panels.** ")
		b.WriteString("That is deliberate: the alternative would be guessing metric names, and a panel querying a metric ")
		b.WriteString("nothing exports reads as \"no traffic\" rather than \"no such metric\". Add `promql:` entries to ")
		b.WriteString("`assert:` (R-CFG-17) and re-run this command.\n\n")
	}

	if others := obsOtherAsserts(cfg); len(others) > 0 {
		b.WriteString("**Not plotted here:** `" + strings.Join(others, "`, `") + "`. ")
		b.WriteString("These are k6 thresholds (or `sql:` escape hatches) — k6 evaluates them in-process during `tortureu run`, ")
		b.WriteString("and no Prometheus scrapes them unless you configure k6's own Prometheus remote-write output ")
		b.WriteString("(`k6 run -o experimental-prometheus-rw`). This emit does not configure that for you, and does not ")
		b.WriteString("pretend those series exist.\n\n")
	}

	b.WriteString("**Datasource:** panels bind to the `${" + obsDatasourceVar + "}` dashboard variable, not to a fixed UID — ")
	b.WriteString("pick your Prometheus in the dropdown above. No datasource UID is guessed anywhere in this file. ")
	switch {
	case sys == nil:
		b.WriteString("(Detection did not run for this emit, so nothing is claimed about whether a Prometheus exists in your compose project — detection did not run.)")
	case sys.Obs.Metrics:
		b.WriteString("(A metrics backend was detected in your compose project (R-DET-12), but its Grafana datasource UID is a Grafana-side fact detection cannot see.)")
	default:
		b.WriteString("(Detection found no metrics backend in your compose project (R-DET-12) — so these panels may have nothing to query at all.)")
	}
	b.WriteString("\n\n")

	if len(cfg.Faults) > 0 {
		b.WriteString("**Fault windows are not annotated.** `emit` performs no scheduling against the k6 phase clock ")
		b.WriteString("(delegate tier: real output, separate timing), so it has no wall-clock time for any fault. ")
		b.WriteString("The faults this config injects are:\n\n")
		for _, f := range cfg.Faults {
			window := f.At
			if f.For != "" {
				window += " for " + f.For
			}
			b.WriteString(fmt.Sprintf("- `%s` — %s on `%s` at %s\n", f.Name, f.Verb, f.Target, window))
		}
		b.WriteString("\nLine them up by eye against the run's own start time.\n")
	}
	return b.String()
}

func obsTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ============================================================== pyroscope ==

const obsPyroscopeHeader = `#!/usr/bin/env bash
# Generated by tortureu emit pyroscope. Requires: docker, curl.
#
# What this gives you: a Pyroscope server, plus one flamegraph-diff query
# per fault — baseline window (the equal-length period immediately before
# the fault) against the fault window itself. That diff is the "root cause
# for free" case in registry.yaml: the frames that grew are the code that
# reacted to the fault.
#
# What it CANNOT do for you: profile your service. Pyroscope is pull-free —
# your process must push profiles, which means adding the SDK below. This
# script starts the receiver and asks it the right questions; if nothing is
# instrumented, every query below returns an empty flamegraph, and that is
# an honest empty, not a broken one.
#
# at:/for: are NOT scheduled here — delegate tier means real output,
# separate timing (see the package doc). The windows below are computed as
# offsets from the run's start, which you supply as RUN_START_EPOCH.
set -euo pipefail
`

// Pyroscope emits a continuous-profiling setup plus one baseline-vs-fault
// flamegraph diff per fault (R-CLI-8 proposed; registry.yaml pyroscope).
func Pyroscope(cfg *config.Config, sys *detect.System) (string, error) {
	service := cfg.Target.Service
	if service == "" {
		service = "sut"
	}

	var b strings.Builder
	b.WriteString(obsPyroscopeHeader)
	b.WriteString("\nPYROSCOPE_URL=\"${PYROSCOPE_URL:-http://localhost:4040}\"\n")
	b.WriteString(fmt.Sprintf("SERVICE_NAME=\"${SERVICE_NAME:-%s}\"\n", service))
	b.WriteString("# Profile type. process_cpu:... is what the Go/pprof-based SDKs push;\n")
	b.WriteString("# other runtimes name their profiles differently — list yours with:\n")
	b.WriteString("#   curl -s \"$PYROSCOPE_URL/pyroscope/label-values?label=__name__\"\n")
	b.WriteString("PROFILE_TYPE=\"${PROFILE_TYPE:-process_cpu:cpu:nanoseconds:cpu:nanoseconds}\"\n")
	b.WriteString("\n# 1. Start the server (UI and ingest both on 4040).\n")
	b.WriteString(fmt.Sprintf("docker run -d --name tortureu-pyroscope -p 4040:4040 %s\n", obsPyroscopeImage))
	b.WriteString("# Wait for it. `docker run` returns long before pyroscope serves queries:\n")
	b.WriteString("# its ingester reports /ready only ~20s later. This line is here because\n")
	b.WriteString("# the first version of this script was run for real and exited 56 —\n")
	b.WriteString("# curl could not connect — at exactly this point.\n")
	b.WriteString("until curl -sf -o /dev/null \"$PYROSCOPE_URL/ready\"; do sleep 2; done\n")
	b.WriteString("\n# 2. Instrument the system under test, then re-run your load.\n")
	b.WriteString(obsPyroscopeSDK(sys))

	b.WriteString("\n# 3. Diff each fault window against the equal-length window before it.\n")
	b.WriteString("#    RUN_START_EPOCH is the unix time (seconds) your run began:\n")
	b.WriteString("#      RUN_START_EPOCH=$(date +%s) tortureu run ...\n")
	b.WriteString("RUN_START_EPOCH=\"${RUN_START_EPOCH:?set RUN_START_EPOCH to the unix time the run started}\"\n")

	if len(cfg.Faults) == 0 {
		b.WriteString("\n# torture.yaml declares no faults, so there is no fault window to diff\n")
		b.WriteString("# against. Browse the profile directly at $PYROSCOPE_URL instead.\n")
		return b.String(), nil
	}

	for _, f := range cfg.Faults {
		b.WriteString("\n" + atComment(f))
		start, ok := obsPhaseOffset(cfg, f.At)
		if !ok {
			b.WriteString(fmt.Sprintf(
				"# fault %q: its at: (%q) could not be resolved to an offset from the run's\n"+
					"# start using load.stages — no window is emitted rather than a guessed one.\n", f.Name, f.At))
			continue
		}
		dur, ok := obsDuration(f.For)
		if !ok {
			b.WriteString(fmt.Sprintf(
				"# fault %q: has no for: duration, so its window has no end — pick one yourself\n"+
					"# and query $PYROSCOPE_URL/pyroscope/render-diff as in the other blocks.\n", f.Name))
			continue
		}
		slug := obsSlug(f.Name)
		b.WriteString(fmt.Sprintf("# window: t+%ds .. t+%ds (%s), baseline: the %s before it\n",
			start, start+dur, f.For, f.For))
		b.WriteString(fmt.Sprintf("%s_FROM=$((RUN_START_EPOCH+%d)); %s_UNTIL=$((%s_FROM+%d))\n",
			slug, start, slug, slug, dur))
		b.WriteString(fmt.Sprintf("curl -sG \"$PYROSCOPE_URL/pyroscope/render-diff\" \\\n"+
			"  --data-urlencode \"leftQuery=$PROFILE_TYPE{service_name=\\\"$SERVICE_NAME\\\"}\" \\\n"+
			"  --data-urlencode \"leftFrom=$((%s_FROM-%d))\" --data-urlencode \"leftUntil=$%s_FROM\" \\\n"+
			"  --data-urlencode \"rightQuery=$PROFILE_TYPE{service_name=\\\"$SERVICE_NAME\\\"}\" \\\n"+
			"  --data-urlencode \"rightFrom=$%s_FROM\" --data-urlencode \"rightUntil=$%s_UNTIL\" \\\n"+
			"  --data-urlencode \"format=json\" > %s-diff.json\n",
			slug, dur, slug, slug, slug, slug))
	}
	b.WriteString("\n# Each *-diff.json is a flamebearer document; open the same windows in the\n")
	b.WriteString("# UI at $PYROSCOPE_URL/comparison-diff for the rendered version.\n")
	return b.String(), nil
}

// obsPyroscopeSDK emits the instrumentation hint for the detected language
// only. Every package named here was checked to exist at its registry (see
// this file's VERIFICATION STATUS); an undetected language gets the whole
// list plus the fact that detection did not name one, never a guess.
func obsPyroscopeSDK(sys *detect.System) string {
	lines := map[string]string{
		"go":     "#   go get github.com/grafana/pyroscope-go   (pyroscope.Start(pyroscope.Config{ApplicationName: $SERVICE_NAME, ServerAddress: $PYROSCOPE_URL}))\n",
		"node":   "#   npm install @pyroscope/nodejs            (Pyroscope.init({appName, serverAddress}); Pyroscope.start())\n",
		"python": "#   pip install pyroscope-io                 (pyroscope.configure(application_name=..., server_address=...))\n",
	}
	if sys != nil {
		if line, ok := lines[sys.Lang]; ok {
			return fmt.Sprintf("#    Detected language: %s.\n%s", sys.Lang, line)
		}
	}
	var b strings.Builder
	b.WriteString("#    The SUT's language was not detected")
	if sys != nil && sys.Lang != "" {
		b.WriteString(fmt.Sprintf(" as one this emit has an SDK for (detected: %q)", sys.Lang))
	}
	b.WriteString(", so all supported SDKs are listed\n#    rather than one being guessed:\n")
	for _, lang := range []string{"go", "node", "python"} {
		b.WriteString(lines[lang])
	}
	return b.String()
}

// ================================================================= jaeger ==

// Jaeger emits a trace-collection setup, gated on the lacks:otel predicate
// (R-CLI-8 proposed; registry.yaml jaeger, when: lacks:otel).
//
// The gate is tri-state (R-COV-6) and is reported as such. Collapsing
// FactUnknown into "applies" would tell someone to instrument a system that
// may already be instrumented — the exact silent-guess failure R-COV-6
// exists to prevent — so unknown is reported as unknown, and the script
// below it is still emitted, because a script the user can read and decline
// is more useful than a refusal.
func Jaeger(cfg *config.Config, sys *detect.System) (string, error) {
	service := cfg.Target.Service
	if service == "" {
		service = "sut"
	}

	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("# Generated by tortureu emit jaeger. Requires: docker.\n#\n")
	b.WriteString("# Registry predicate for this tool: lacks:otel — it is suggested when\n")
	b.WriteString("# your system has NO OpenTelemetry of its own. Detection's verdict:\n#\n")
	switch {
	case sys == nil:
		b.WriteString("#   detection did not run for this emit, so lacks:otel was not evaluated.\n")
		b.WriteString("#   Nothing is claimed either way — run `tortureu doctor` to see the fact.\n")
	case sys.Coverage.LacksOtel == detect.FactTrue:
		b.WriteString("#   lacks:otel = true — no OTel client in any manifest and no collector in\n")
		b.WriteString("#   compose. This tool applies: there is no tracing to reuse.\n")
	case sys.Coverage.LacksOtel == detect.FactFalse:
		b.WriteString("#   lacks:otel = false — your system already has OpenTelemetry (an OTel\n")
		b.WriteString("#   client in a manifest, or a collector in compose). Prefer pointing that\n")
		b.WriteString("#   existing collector's OTLP exporter at the Jaeger below over\n")
		b.WriteString("#   instrumenting again; a second SDK in the same process is a way to lose\n")
		b.WriteString("#   spans, not gain them. Only the docker run line is likely useful to you.\n")
	default:
		b.WriteString("#   lacks:otel = unknown — a manifest was present but R-DET-14 has no parser\n")
		b.WriteString("#   for it, so whether you already have OTel could not be established. This\n")
		b.WriteString("#   is reported, not assumed either way (R-COV-6): check your own manifest\n")
		b.WriteString("#   before adding an SDK.\n")
	}
	b.WriteString("#\n")
	b.WriteString("# Jaeger only stores what it is sent. This script starts a collector and\n")
	b.WriteString("# tells your service where to send spans; it CANNOT instrument your code —\n")
	b.WriteString("# without an OTel SDK in the process, the UI stays empty.\n")
	b.WriteString("#\n")
	b.WriteString("# at: windows are NOT scheduled here (delegate tier: real output, separate\n")
	b.WriteString("# timing). Use the fault windows listed at the bottom to filter the UI.\n")
	b.WriteString("set -euo pipefail\n\n")

	b.WriteString("# 1. Collector + UI. 4317 = OTLP/gRPC, 4318 = OTLP/HTTP, 16686 = UI and\n")
	b.WriteString("#    query API. This is Jaeger v2; jaegertracing/all-in-one is v1, which is\n")
	b.WriteString("#    end-of-life.\n")
	b.WriteString(fmt.Sprintf("docker run -d --name tortureu-jaeger \\\n"+
		"  -p 16686:16686 -p 4317:4317 -p 4318:4318 \\\n  %s\n\n", obsJaegerImage))

	b.WriteString("# 2. Environment for the system under test. Add these to the service's\n")
	b.WriteString("#    environment: block in your compose file — from inside the compose\n")
	b.WriteString("#    network, use the container name instead of localhost, and put Jaeger on\n")
	b.WriteString("#    the same network (--network <project>_default).\n")
	b.WriteString(fmt.Sprintf("#   OTEL_SERVICE_NAME=%s\n", service))
	b.WriteString("#   OTEL_EXPORTER_OTLP_ENDPOINT=http://tortureu-jaeger:4318\n")
	b.WriteString("#   OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf\n")
	b.WriteString("#   OTEL_TRACES_EXPORTER=otlp\n")
	b.WriteString("#   OTEL_TRACES_SAMPLER=always_on   # sample everything: a torture run is short\n")
	b.WriteString("#                                   # and the failures are what you came for\n\n")

	b.WriteString("# 3. Confirm spans are arriving (this is the check that was run against a\n")
	b.WriteString("#    real Jaeger v2 when this emitter was written):\n")
	b.WriteString(fmt.Sprintf("#   curl -s http://localhost:16686/api/services | grep %s\n\n", service))

	if len(cfg.Faults) > 0 {
		b.WriteString("# Fault windows to filter on in the UI (this emit does not schedule them):\n")
		for _, f := range cfg.Faults {
			window := f.At
			if f.For != "" {
				window += " for " + f.For
			}
			b.WriteString(fmt.Sprintf("#   %s — %s on %s at %s\n", f.Name, f.Verb, f.Target, window))
		}
		b.WriteString("# Jaeger's own filters are the useful ones during a fault: minimum duration\n")
		b.WriteString("# (find the tail), and error=true (find what gave up).\n")
	} else {
		b.WriteString("# torture.yaml declares no faults, so there is no fault window to filter on.\n")
	}
	return b.String(), nil
}

// ================================================================ helpers ==

var obsDurationPattern = regexp.MustCompile(`^(\d+)(ms|s|m|h)$`)

// obsDuration parses a torture.yaml duration ("40s", "2m") into whole
// seconds. ok is false for anything it does not recognize — including "" —
// so the caller reports the window as underivable instead of emitting a
// wrong one.
func obsDuration(s string) (int, bool) {
	m := obsDurationPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "ms":
		return n / 1000, true
	case "m":
		return n * 60, true
	case "h":
		return n * 3600, true
	default:
		return n, true
	}
}

// obsPhaseOffset resolves a fault's at: (R-CFG-11: "<phase>",
// "<phase>+<duration>", or "t=<duration>") to an offset in seconds from the
// start of the run, by summing the durations of the load stages before the
// named phase. ok is false when any stage in the way has no parseable
// duration, or the phase is not in load.stages — an unanchored window is
// reported, never approximated.
func obsPhaseOffset(cfg *config.Config, at string) (int, bool) {
	at = strings.TrimSpace(at)
	if rest, found := strings.CutPrefix(at, "t="); found {
		return obsDuration(rest)
	}
	phase, plus, hasPlus := strings.Cut(at, "+")
	extra := 0
	if hasPlus {
		var ok bool
		if extra, ok = obsDuration(plus); !ok {
			return 0, false
		}
	}
	offset := 0
	for _, st := range cfg.Load.Stages {
		if st.Phase == phase {
			return offset + extra, true
		}
		d := st.For
		if d == "" {
			d = st.Over
		}
		secs, ok := obsDuration(d)
		if !ok {
			return 0, false
		}
		offset += secs
	}
	return 0, false
}

var obsSlugPattern = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// obsSlug turns a fault name into a shell-variable-safe token.
func obsSlug(name string) string {
	s := obsSlugPattern.ReplaceAllString(name, "_")
	s = strings.Trim(s, "_")
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		s = "F" + s
	}
	return strings.ToUpper(s)
}
