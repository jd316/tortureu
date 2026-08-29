// RESTler — stateful REST API fuzzing from an OpenAPI document
// (registry.yaml: tier delegate, when: spec:openapi, how: "tortureu emit
// restler", note "stateful: infers request SEQUENCES"). R-CLI-8 proposed.
//
// Why this exists next to `run --fuzz` (R-EXE-27, schemathesis): the two are
// not the same tool wearing different names. Schemathesis fuzzes ONE
// operation at a time against its schema. RESTler compiles the spec into a
// grammar, infers producer-consumer dependencies between operations (a POST
// that returns an id, a GET that needs one) and fuzzes SEQUENCES — which is
// the only way to reach the states behind a create. That is the registry's
// "infers request SEQUENCES", and it is also why this is a separate handoff
// rather than another flag on `run`: it needs its own timing and its own
// blast-radius decision.
//
// VERIFICATION STATUS (what was actually run on this host, 2026-08-09):
//
//   - VERIFIED: the official image was pulled —
//     mcr.microsoft.com/restlerfuzzer/restler:v9.2.4, digest
//     sha256:f37705fc4a11071f1de67ac4708fd97c71c1b4e5a0bc0f9c16c21028c0c41cbb
//     — and reports "RESTler version: 9.2.4". The published image LAGS the
//     repository, whose newest tag is v9.3.1 and for which no image is
//     published at all, so this pins the newest thing that can be pulled.
//   - VERIFIED: the compile config this file emits, byte for byte, was fed
//     to that real image (`dotnet /RESTler/restler/Restler.dll compile
//     /tortureu-work/compile-config.json`) over a real OpenAPI 3.0 document.
//     It exited 0 with an empty StdErr.txt, logged "Workflow completed", and
//     wrote grammar.py (127 lines), grammar.json, dict.json,
//     dependencies.json and its own engine_settings.json.
//   - VERIFIED, the registry's "infers request SEQUENCES" claim itself: for
//     a spec whose POST /orders returns {id} and whose GET/DELETE
//     /orders/{orderId} take a path parameter, the compiler's
//     dependencies.json resolved orderId to {"producer_endpoint":
//     "/orders", "producer_method": "POST", "producer_resource_name":
//     "id"}. That inference is the whole reason this handoff exists next to
//     schemathesis, and it was observed rather than assumed.
//   - VERIFIED by reading the tool's own source rather than docs: the
//     compiler config keys are PascalCase F# record fields
//     (Restler.Compiler/Config.fs) and the engine settings keys are
//     snake_case (docs/user-guide/SettingsFile.md). `ApiSpecs` — a
//     plausible-looking key — does not exist; the per-spec construct is
//     `SwaggerSpecConfig`.
//   - NOT VERIFIED: no fuzzing run was performed against a real service.
//     `restler test`/`fuzz` need a live SUT, and fuzzing one is precisely the
//     act this file refuses to perform on the user's behalf. Nothing here
//     claims a bug would be found, or that the container can reach any
//     particular SUT.
//   - NOT VERIFIED: the bind-mount line in the emitted script. This host's
//     Docker refuses mounts outside the project directory, so verification
//     used `docker cp` into a running container instead. The mount is the
//     ordinary form and is what a user needs; it was not exercised here, and
//     this comment is the record of that.
//
// What it refuses to invent:
//
//   - The spec path. It is torture.yaml's target.openapi and nothing else.
//     R-EXE-27 already refuses to find it by scanning for conventional
//     filenames, and fuzzing the wrong document produces confident findings
//     about an API that does not exist.
//   - The target. host/target_port/no_ssl come from target.base_url. An
//     https:// URL never becomes no_ssl, and a URL with no port never gains
//     one (RESTler's own default is 443 with TLS, 80 without — its default
//     to apply, not ours).
//   - A time budget. `restler fuzz` defaults to 168 hours; any shorter value
//     picked here would be a schedule invented for someone else's API. Fuzz
//     mode therefore requires TORTUREU_RESTLER_TIME_BUDGET_HOURS explicitly.
//   - Credentials, checkers, and combination limits. torture.yaml carries no
//     token and no fuzzing policy, so `authentication`, `checkers`,
//     `max_combinations` and friends are absent from the emitted settings
//     rather than filled with a guess.
//
// Fault translation: none. RESTler sends requests; it injects nothing. Every
// fault in torture.yaml is reported per fault with the emitter that does
// translate it (R-CLI-8).
package emit

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/fault"
)

// restlerImage is the exact image this file's verification ran against.
// Pinned to what Microsoft publishes: the repository's newest tag (v9.3.1)
// has no image, so :v9.2.4 is the newest thing that can actually be pulled,
// and pinning it keeps the claim above and the emitted script together.
const restlerImage = "mcr.microsoft.com/restlerfuzzer/restler:v9.2.4"

const restlerHeader = `#!/usr/bin/env bash
# Generated by tortureu emit restler. Requires: docker.
#
#   bash this-script.sh            # coverage pass: restler test (one sequence per request)
#   bash this-script.sh fuzz       # full stateful fuzz; needs TORTUREU_RESTLER_TIME_BUDGET_HOURS
#
# RESTler compiles your OpenAPI document into a grammar, infers which
# operations produce the values other operations consume, and fuzzes those
# SEQUENCES. That is what it does that a per-operation fuzzer cannot.
#
# ┌─ READ THIS BEFORE RUNNING ─────────────────────────────────────────────┐
# │ RESTler issues REAL requests, including POST, PUT, PATCH and DELETE,   │
# │ against the address below. It creates and destroys resources on        │
# │ purpose — that is how it reaches states behind a create. Point it at a │
# │ disposable environment. Nothing in torture.yaml can tell us whether    │
# │ this address is one, so this script will not decide that for you.      │
# └────────────────────────────────────────────────────────────────────────┘
#
# No credentials are emitted: torture.yaml carries none, and RESTler's
# authentication settings (token_refresh_cmd / authentication{}) need a
# command only you can supply. An API behind auth will fuzz as an
# unauthenticated client until you add one.
#
# at:/for: are NOT scheduled by this script — this is a delegate-tier handoff
# (real output, separate timing, registry.yaml).
set -euo pipefail
`

// restlerCompileConfig is the compiler config document. Field names are the
// PascalCase F# record fields RESTler's Restler.Compiler/Config.fs declares;
// everything optional is omitted rather than restated with its own default,
// so the emitted file says only what this repo actually knows.
type restlerCompileConfig struct {
	SwaggerSpecFilePath []string `json:"SwaggerSpecFilePath"`
}

// restlerEngineSettings is the engine settings document (snake_case keys,
// docs/user-guide/SettingsFile.md). Only the three fields derivable from
// target.base_url appear; omitempty keeps an https target from carrying a
// no_ssl:false that reads as a decision rather than an absence.
type restlerEngineSettings struct {
	Host       string `json:"host,omitempty"`
	TargetPort int    `json:"target_port,omitempty"`
	NoSSL      bool   `json:"no_ssl,omitempty"`
}

// restlerTarget is what target.base_url yields, with https recorded so an
// absent port is left to RESTler's own default rather than assumed.
type restlerTarget struct {
	host  string
	port  int
	https bool
}

// restlerParseBaseURL splits target.base_url into the three engine settings
// RESTler understands. ok is false when there is no host to fuzz.
func restlerParseBaseURL(baseURL string) (restlerTarget, bool) {
	s := strings.TrimSpace(baseURL)
	if s == "" {
		return restlerTarget{}, false
	}
	scheme, rest, hasScheme := strings.Cut(s, "://")
	if !hasScheme {
		rest, scheme = s, "http"
	}
	hostport, _, _ := strings.Cut(rest, "/")
	host, portStr := hostPort(hostport)
	if host == "" {
		return restlerTarget{}, false
	}
	t := restlerTarget{host: host, https: strings.EqualFold(scheme, "https")}
	if portStr != "" {
		n, err := strconv.Atoi(portStr)
		if err != nil {
			return restlerTarget{}, false
		}
		t.port = n
	}
	return t, true
}

// RESTler emits the stateful-fuzz handoff described in this file's header.
func RESTler(cfg *config.Config, sys *detect.System) (string, error) {
	if sys == nil {
		return "# tortureu emit restler: the system could not be detected, so whether this repo has\n" +
			"# an OpenAPI document (spec:openapi) is unknown — which is not the same as knowing it\n" +
			"# has none; nothing to emit.\n", nil
	}
	if !sys.Coverage.OpenAPI {
		return "# tortureu emit restler: detection reports spec:openapi false — no OpenAPI or Swagger\n" +
			"# document was found in this repo, and RESTler fuzzes nothing else; nothing to emit.\n", nil
	}
	spec := strings.TrimSpace(cfg.Target.OpenAPI)
	if spec == "" {
		return "# tortureu emit restler: detection found an OpenAPI document somewhere in this repo,\n" +
			"# but torture.yaml's target.openapi does not say which file it is. Set target.openapi.\n" +
			"# TortureU does not pick the document by scanning for conventional filenames (the same\n" +
			"# rule R-EXE-27 applies to --fuzz): fuzzing the wrong spec yields confident findings\n" +
			"# about an API that does not exist; nothing to emit.\n", nil
	}
	target, ok := restlerParseBaseURL(cfg.Target.BaseURL)
	if !ok {
		return "# tortureu emit restler: torture.yaml declares no usable target.base_url, so there is\n" +
			"# no host to fuzz and no address that may be guessed; nothing to emit.\n", nil
	}

	specName := restlerContainerSpecName(spec)
	compile, err := json.MarshalIndent(restlerCompileConfig{
		SwaggerSpecFilePath: []string{specName},
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("emit restler: %w", err)
	}
	settings := restlerEngineSettings{Host: target.host, TargetPort: target.port, NoSSL: !target.https}
	engine, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("emit restler: %w", err)
	}

	var faultNotes strings.Builder
	for _, f := range cfg.Faults {
		if _, terr := fault.Translate(f); terr != nil {
			return "", fmt.Errorf("emit restler: %w", terr)
		}
		faultNotes.WriteString(atComment(f))
		faultNotes.WriteString(skipComment("restler", f,
			"RESTler sends requests and injects nothing; use \"tortureu emit pumba\", \"netem\" or \"iptables\" for this fault"))
	}

	var b strings.Builder
	b.WriteString(restlerHeader)

	fmt.Fprintf(&b, `
# The document to compile: torture.yaml's target.openapi, verbatim.
spec_host_path=%q
if [ ! -f "$spec_host_path" ]; then
  echo "tortureu emit restler: no such OpenAPI document: $spec_host_path" >&2
  echo "  (this is torture.yaml's target.openapi; it is resolved relative to where you run this)" >&2
  exit 2
fi
work="${TORTUREU_RESTLER_WORKDIR:-./restler-work}"
mkdir -p "$work"
mode="${1:-test}"
`, spec)

	fmt.Fprintf(&b, `
cat > "$work/compile-config.json" <<'RESTLER_COMPILE_CONFIG'
%s
RESTLER_COMPILE_CONFIG
`, compile)

	fmt.Fprintf(&b, `
# Engine settings. host/target_port/no_ssl are derived from target.base_url
# (%s) and nothing else. Every other setting RESTler has — checkers,
# max_combinations, per_resource_settings, authentication — is deliberately
# absent: torture.yaml states no fuzzing policy, and a default written here
# would read as a decision this project made about your API.
cat > "$work/engine-settings.json" <<'RESTLER_ENGINE_SETTINGS'
%s
RESTLER_ENGINE_SETTINGS
`, cfg.Target.BaseURL, engine)

	fmt.Fprintf(&b, `
cp "$spec_host_path" "$work/%s"

# --network: RESTler runs in a container and must reach the address above.
# "host" is the default because target.base_url usually names a published
# port. A SUT isolated on the internal-only network R-DC2-3 requires is NOT
# reachable this way — the same limitation SPEC.md §12's TBD-12 records for
# --db-load and --fuzz, and for the same reason: a subprocess (or here, a
# separate container) dials from its own namespace. Set
# TORTUREU_RESTLER_DOCKER_NETWORK=container:<id> to join the SUT's namespace.
network="${TORTUREU_RESTLER_DOCKER_NETWORK:-host}"
image="${TORTUREU_RESTLER_IMAGE:-%s}"

run_restler() {
  docker run --rm \
    --network "$network" \
    -v "$(cd "$work" && pwd)":/tortureu-work \
    -w /tortureu-work \
    "$image" \
    dotnet /RESTler/restler/Restler.dll "$@"
}

# Compile: spec -> grammar. This is where the producer-consumer inference
# that makes RESTler stateful happens; it talks to nothing.
run_restler compile /tortureu-work/compile-config.json
`, specName, restlerImage)

	b.WriteString(`
grammar=/tortureu-work/Compile/grammar.py
dictionary=/tortureu-work/Compile/dict.json

case "$mode" in
  test)
    # One sequence per request: RESTler's own smoke pass. It reports which
    # operations it could reach at all, which is the honest first question —
    # a fuzzer that never got past authentication finds nothing and says so.
    run_restler test \
      --grammar_file "$grammar" \
      --dictionary_file "$dictionary" \
      --settings /tortureu-work/engine-settings.json
    ;;
  fuzz)
    if [ -z "${TORTUREU_RESTLER_TIME_BUDGET_HOURS:-}" ]; then
      echo "tortureu emit restler: fuzz mode needs TORTUREU_RESTLER_TIME_BUDGET_HOURS." >&2
      echo "  RESTler's own default is 168 hours (one week). torture.yaml says nothing about" >&2
      echo "  how long you are willing to fuzz, and picking a number here would be inventing" >&2
      echo "  a schedule for your API. Set it and re-run." >&2
      exit 2
    fi
    run_restler fuzz \
      --grammar_file "$grammar" \
      --dictionary_file "$dictionary" \
      --settings /tortureu-work/engine-settings.json \
      --time_budget "$TORTUREU_RESTLER_TIME_BUDGET_HOURS"
    ;;
  *)
    echo "usage: $0 [test|fuzz]" >&2
    exit 2 ;;
esac

# Findings land in $work/{Test,Fuzz}/RestlerResults/experiment*/bug_buckets/.
# Each bug bucket carries a .replay.txt that "restler replay --replay_log"
# re-sends, which is how a finding is confirmed rather than believed.
echo "tortureu emit restler: results under $work" >&2
`)

	if faultNotes.Len() > 0 {
		b.WriteString("\n# faults declared in torture.yaml that this emit does NOT translate\n" +
			"# (listed, never dropped — R-CLI-8):\n")
		b.WriteString(faultNotes.String())
	}
	return b.String(), nil
}

// restlerContainerSpecName is the name the spec is copied to inside the
// container: a fixed stem (the user's own filename may contain spaces or a
// leading dash, and it is written into a JSON document RESTler parses) but
// the user's own EXTENSION, because RESTler dispatches its parser on it and
// renaming a YAML spec to .json would make it fail to load.
func restlerContainerSpecName(spec string) string {
	ext := filepath.Ext(spec)
	if ext == "" {
		ext = ".json"
	}
	return "openapi-spec-under-test" + ext
}

func init() {
	// needsSystem: true. registry.yaml gates restler on spec:openapi, which
	// is a detection fact (Coverage.OpenAPI) — and this emitter has to be
	// able to tell "detection did not run" from "this repo has no spec",
	// which is only possible if it is handed the *detect.System itself.
	Register("restler", RESTler, true)
}
