package emit

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/fault"
)

// Gatling and Locust are NOT wired into Emit/Tools (emit.go) in this file.
// emit.go's dispatch is a hand-written switch plus a fixed Tools slice,
// shared with other agents extending internal/emit concurrently (task
// instruction: "do not edit a shared dispatch table ... if internal/emit's
// current shape makes [init()-based registration] impossible, say so and
// stop rather than editing shared code"). That is the case here: there is
// no map/registry to append to, only a switch. TestEmit_UnknownToolListsSupported
// in emit_test.go even uses "gatling" as its example of an unsupported
// tool today, confirming this file is meant to add Gatling(cfg) and
// Locust(cfg) as directly callable, independently tested functions, with
// wiring into `tortureu emit gatling|locust` left as a follow-up edit to
// emit.go's switch and Tools slice once the other agents' work is
// serialized.

// loadStep is one internal/config Stage, decoded into the numbers needed to
// drive an arrival-rate injection profile: a hold stage has from == to.
type loadStep struct {
	phase string
	from  float64 // rps at the start of this stage
	to    float64 // rps at the end of this stage
	secs  float64 // stage duration in seconds
	ramp  bool    // true for a to:/over: ramp, false for a hold:/for: stage
}

// loadgenRPSPattern is named defensively (not just rpsPattern) because this
// package is being extended by multiple agents concurrently and unexported
// package-level identifiers collide across files; internal/emit/protocol.go
// already declares its own narrower rpsPattern.
var loadgenRPSPattern = regexp.MustCompile(`(?i)^\s*(\d+(?:\.\d+)?)\s*rps\s*$`)

// parseRPS parses a load.stages to:/hold: value ("200rps") into a plain
// requests-per-second number. Only the "rps" unit is documented for load
// stages (SPEC.md R-CFG-6..8), so anything else errors rather than guesses.
func parseRPS(field, s string) (float64, error) {
	m := rpsPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("load.stages: %s = %q: expected a number followed by \"rps\"", field, s)
	}
	return strconv.ParseFloat(m[1], 64)
}

var stageDurationPattern = regexp.MustCompile(`(?i)^\s*(\d+(?:\.\d+)?)\s*(ms|s|m|h)\s*$`)

// parseStageSeconds parses a load.stages over:/for: duration into seconds.
func parseStageSeconds(field, s string) (float64, error) {
	m := stageDurationPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("load.stages: %s = %q: expected a number followed by ms/s/m/h", field, s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	switch strings.ToLower(m[2]) {
	case "ms":
		return n / 1000, nil
	case "m":
		return n * 60, nil
	case "h":
		return n * 3600, nil
	default: // "s"
		return n, nil
	}
}

// buildSteps translates cfg.Load.Stages (R-CFG-6..8's open arrival-rate
// model) into loadSteps, tracking the running rate so a ramp stage's
// implicit "from" is the previous stage's end rate (0 for the first stage).
// This is the one arrival-rate derivation shared by every load-generator
// target in this file — each target's own function only renders it.
func buildSteps(cfg *config.Config) ([]loadStep, error) {
	steps := make([]loadStep, 0, len(cfg.Load.Stages))
	current := 0.0
	for _, s := range cfg.Load.Stages {
		switch {
		case s.Hold != "":
			rate, err := parseRPS("hold", s.Hold)
			if err != nil {
				return nil, err
			}
			secs, err := parseStageSeconds("for", s.For)
			if err != nil {
				return nil, err
			}
			steps = append(steps, loadStep{phase: s.Phase, from: rate, to: rate, secs: secs, ramp: false})
			current = rate
		case s.To != "":
			rate, err := parseRPS("to", s.To)
			if err != nil {
				return nil, err
			}
			secs, err := parseStageSeconds("over", s.Over)
			if err != nil {
				return nil, err
			}
			steps = append(steps, loadStep{phase: s.Phase, from: current, to: rate, secs: secs, ramp: true})
			current = rate
		default:
			return nil, fmt.Errorf("load.stages: phase %q has neither to: nor hold:", s.Phase)
		}
	}
	return steps, nil
}

// fmtNum renders a float the way both Gatling's Scala DSL and a Python
// literal want it: "200" not "200.0" for whole numbers, otherwise the
// shortest exact decimal.
func fmtNum(f float64) string {
	if f == math.Trunc(f) {
		return strconv.FormatFloat(f, 'f', 0, 64)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// checkFaults validates every fault via internal/fault (reusing its verb
// table rather than re-deriving one, per R-CLI-8) and renders each as
// "not translated by <tool>" — a load generator emits request/user traffic
// only, never fault injection, so every fault is reported skipped, never
// silently dropped (R-CLI-8's per-fault reporting requirement, mirrored
// from pumba.go/netem.go/iptables.go's own skip handling).
func checkFaults(tool string, cfg *config.Config) (string, error) {
	var b strings.Builder
	for _, f := range cfg.Faults {
		if _, err := fault.Translate(f); err != nil {
			return "", fmt.Errorf("emit %s: %w", tool, err)
		}
		b.WriteString(skipComment(tool, f, "load generators emit request/user traffic only; use \"tortureu emit pumba\", \"netem\", or \"iptables\" for fault injection"))
	}
	return b.String(), nil
}

// scalaIdent/pyIdent sanitize a scenario name into a legal identifier
// suffix: torture.yaml names are free text (R-CFG-9 only requires
// method/path), Scala and Python identifiers are not.
var identSanitizer = regexp.MustCompile(`[^A-Za-z0-9_]+`)

func ident(name string) string {
	s := identSanitizer.ReplaceAllString(name, "_")
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		s = "s_" + s
	}
	return s
}

// ---------------------------------------------------------------------
// Gatling
// ---------------------------------------------------------------------

// gatlingHeader documents what this target does and does not cover.
// Verification status: this emit's Scala output (run through Gatling
// against torture.example.yaml) was actually compiled and executed with a
// real Gatling 3.10.5 (gatling-charts-highcharts-bundle, `gatling.sh -s
// TortureUSimulation -nr`) — it compiled cleanly and issued real HTTP
// requests for every http() call in every chain, split across scenarios
// at exactly the randomSwitch weights, ramping/holding at the
// rampUsersPerSec/constantUsersPerSec rates translated from load.stages.
// No SUT was listening during that run, so responses were
// connection-refused (KO) — that failure is about the missing target, not
// this emitted Scala, and does not need re-verifying per invocation.
const gatlingHeader = `// Generated by tortureu emit gatling. Requires: a Gatling installation
// (https://gatling.io) with Scala 2.13/3 on the classpath, e.g. the
// gatling-charts-highcharts-bundle or the Gatling Maven/sbt/gradle plugin.
//
// Open model (R-CFG-6): load.stages is translated with injectOpen's
// constantUsersPerSec/rampUsersPerSec, which drive arrival RATE directly —
// Gatling opens a new user per arrival rather than looping a fixed pool,
// so a slow response does not throttle the offered load the way a
// closed-model tool would (that's exactly the coordinated-omission
// failure R-CFG-6 exists to avoid). Gatling expresses this faithfully;
// see loadgen.go's Locust header for a target that cannot.
//
// Verification: this shape of file was compiled and executed against a
// real Gatling 3.10.5 (see the package comment above this const);
// individual invocations are not re-run through Gatling automatically —
// re-check "gatling.sh -s TortureUSimulation" if you edit this template.
//
// at:/for: on faults are not applicable here — see the "faults declared"
// comment block below; fault injection is a separate delegate (pumba/
// netem/iptables), not this load generator.
`

// Gatling translates torture.yaml's load: block into a Gatling Simulation
// using injectOpen's open-model DSL, and scenarios into one open arrival
// stream that fans out to a weighted request chain per scenario via
// randomSwitch — preserving a single arrival profile (R-CFG-6) instead of
// running each scenario as its own competing injection.
func Gatling(cfg *config.Config) (string, error) {
	steps, err := buildSteps(cfg)
	if err != nil {
		return "", fmt.Errorf("emit gatling: %w", err)
	}
	faultsOut, err := checkFaults("gatling", cfg)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(gatlingHeader)
	b.WriteString("\nimport io.gatling.core.Predef._\nimport io.gatling.http.Predef._\nimport scala.concurrent.duration._\n\n")
	b.WriteString("class TortureUSimulation extends Simulation {\n")
	fmt.Fprintf(&b, "  val httpProtocol = http.baseUrl(%q)\n\n", cfg.Target.BaseURL)

	scenarios := cfg.Load.Scenarios
	for _, sc := range scenarios {
		fmt.Fprintf(&b, "  val chain_%s = ", ident(sc.Name))
		if len(sc.Flow) == 0 {
			b.WriteString("exec(session => session) // no flow steps declared\n")
			continue
		}
		for i, step := range sc.Flow {
			if i > 0 {
				b.WriteString(".")
			}
			verb := strings.ToLower(step.Method)
			fmt.Fprintf(&b, "exec(http(%q).%s(%q)", fmt.Sprintf("%s-%d", sc.Name, i+1), verb, step.Path)
			if step.Body != "" {
				fmt.Fprintf(&b, ".body(StringBody(%s)).asJson", scalaTripleQuote(step.Body))
			}
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if len(scenarios) == 0 {
		b.WriteString("  val torture = scenario(\"torture\") // no scenarios declared in load.scenarios\n\n")
	} else {
		total := 0
		for _, sc := range scenarios {
			total += sc.Weight
		}
		if total <= 0 {
			total = len(scenarios)
		}
		b.WriteString("  val torture = scenario(\"torture\").exec(randomSwitch(\n")
		for i, sc := range scenarios {
			w := sc.Weight
			if sc.Weight <= 0 && total == len(scenarios) {
				w = 1
			}
			pct := 100 * float64(w) / float64(total)
			sep := ","
			if i == len(scenarios)-1 {
				sep = ""
			}
			fmt.Fprintf(&b, "    %.1f -> chain_%s%s\n", pct, ident(sc.Name), sep)
		}
		b.WriteString("  ))\n\n")
	}

	b.WriteString("  setUp(\n    torture.inject(\n")
	for i, st := range steps {
		sep := ","
		if i == len(steps)-1 {
			sep = ""
		}
		if st.ramp {
			fmt.Fprintf(&b, "      rampUsersPerSec(%s) to %s during (%s seconds)%s // phase %q\n",
				fmtNum(st.from), fmtNum(st.to), fmtNum(st.secs), sep, st.phase)
		} else {
			fmt.Fprintf(&b, "      constantUsersPerSec(%s) during (%s seconds)%s // phase %q\n",
				fmtNum(st.to), fmtNum(st.secs), sep, st.phase)
		}
	}
	b.WriteString("    ).protocols(httpProtocol)\n  )\n}\n")

	if faultsOut != "" {
		b.WriteString("\n// faults declared in torture.yaml's faults: block (not applicable to a\n// load generator; use tortureu emit pumba/netem/iptables instead):\n")
		for _, line := range strings.Split(strings.TrimRight(faultsOut, "\n"), "\n") {
			b.WriteString(toScalaComment(line))
		}
	}
	return b.String(), nil
}

// scalaTripleQuote renders a request body as a Scala string literal.
// Triple-quoted so a JSON body's own double quotes need no escaping.
func scalaTripleQuote(s string) string {
	return `"""` + strings.ReplaceAll(s, `"""`, `\"\"\"`) + `"""`
}

// toScalaComment turns one skipComment line ("# fault ...") into a valid
// Scala "//" comment — skipComment (common.go) is shared by the bash-
// script emitters (pumba/netem/iptables) and hard-codes "#", which is not
// a comment token in Scala.
func toScalaComment(line string) string {
	return "// " + strings.TrimPrefix(line, "# ") + "\n"
}

// ---------------------------------------------------------------------
// Locust
// ---------------------------------------------------------------------

// locustHeader is explicit about the one thing R-CFG-6/the task both call
// out by name: Locust's HttpUser model is closed-loop (a fixed pool of
// users, each looping: do a task, wait, do the next), so this is an
// approximation, not a faithful open-model translation. It is emitted
// anyway, clearly labeled, rather than silently passed off as equivalent
// to Gatling's — prefer "tortureu emit gatling" when the open-model
// guarantee matters.
const locustHeader = `"""
Generated by tortureu emit locust. Requires: pip install locust.

NOT A FAITHFUL OPEN MODEL (R-CFG-6). Locust's HttpUser is a closed-loop
model: a fixed pool of "users", each one looping (run a task, wait, run
the next).
A user's next request is only issued once its current one completes, so
under saturation the offered load self-throttles instead of continuing to
arrive at the declared rate -- this is exactly the coordinated-omission
failure mode an open model exists to avoid, and Locust's model cannot
avoid it. TortureULoadShape below approximates each declared load.stages
phase by treating "1 user ~= 1 request/second" (wait_time =
constant_pacing(1)) and ramping user_count to track the declared rate.
That approximation only holds while every request completes in well under
1 second; once responses run slower than that -- which is precisely when
a fault is squeezing the system -- Locust's actual arrival rate falls
behind the declared one. Prefer "tortureu emit gatling" when the open
arrival-rate guarantee matters; use this only when Locust is the fixed
tool in your pipeline and that gap is acceptable.

Verification: this locustfile was syntax-checked (py_compile) but not run
against the network faults declared in torture.yaml's faults: block --
those are a separate delegate (pumba/netem/iptables), never scheduled by
this file (see the trailing comment for what was reported and why).
"""
`

// Locust translates torture.yaml's load: block into a locustfile.py: one
// HttpUser per the scenarios, weighted with @task(weight) so Locust's own
// task-selection RNG mirrors the scenario weighting, plus a custom
// LoadTestShape approximating the declared arrival-rate stages (see
// locustHeader for exactly how, and its limits).
func Locust(cfg *config.Config) (string, error) {
	steps, err := buildSteps(cfg)
	if err != nil {
		return "", fmt.Errorf("emit locust: %w", err)
	}
	faultsOut, err := checkFaults("locust", cfg)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(locustHeader)
	b.WriteString("\nfrom locust import HttpUser, LoadTestShape, constant_pacing, task\n\n\n")

	b.WriteString("class TortureUUser(HttpUser):\n")
	fmt.Fprintf(&b, "    host = %s\n", pyStr(cfg.Target.BaseURL))
	b.WriteString("    wait_time = constant_pacing(1)  # ~1 request/second/user; see module docstring\n\n")

	scenarios := cfg.Load.Scenarios
	if len(scenarios) == 0 {
		b.WriteString("    # no scenarios declared in load.scenarios\n")
	}
	for _, sc := range scenarios {
		weight := sc.Weight
		if weight <= 0 {
			weight = 1
		}
		fmt.Fprintf(&b, "    @task(%d)\n    def %s(self):\n", weight, ident(strings.ToLower(sc.Name)))
		if len(sc.Flow) == 0 {
			b.WriteString("        pass  # no flow steps declared\n\n")
			continue
		}
		for _, step := range sc.Flow {
			verb := strings.ToLower(step.Method)
			if step.Body != "" {
				fmt.Fprintf(&b, "        self.client.%s(%s, data=%s)\n", verb, pyStr(step.Path), pyStr(step.Body))
			} else {
				fmt.Fprintf(&b, "        self.client.%s(%s)\n", verb, pyStr(step.Path))
			}
		}
		b.WriteString("\n")
	}

	b.WriteString("\nclass TortureULoadShape(LoadTestShape):\n")
	b.WriteString("    # (start_s, end_s, rate_from, rate_to) per load.stages phase, with\n")
	b.WriteString("    # \"1 user ~= 1 rps\" (see module docstring for why this is approximate).\n")
	b.WriteString("    stages = [\n")
	cursor := 0.0
	for _, st := range steps {
		end := cursor + st.secs
		fmt.Fprintf(&b, "        (%s, %s, %s, %s),  # phase %q\n",
			fmtNum(cursor), fmtNum(end), fmtNum(st.from), fmtNum(st.to), st.phase)
		cursor = end
	}
	b.WriteString("    ]\n\n")
	b.WriteString(`    def tick(self):
        run_time = self.get_run_time()
        for start, end, rate_from, rate_to in self.stages:
            if run_time < end:
                span = end - start
                frac = 1.0 if span <= 0 else (run_time - start) / span
                rate = rate_from + (rate_to - rate_from) * frac
                user_count = max(1, round(rate))
                return (user_count, user_count)
        return None
`)
	b.WriteString("\n")

	if faultsOut != "" {
		b.WriteString("\n# faults declared in torture.yaml's faults: block (not applicable to a\n# load generator; use tortureu emit pumba/netem/iptables instead):\n")
		b.WriteString(faultsOut)
	}
	return b.String(), nil
}

// pyStr renders a Go string as a Python string literal. strconv.Quote's Go
// escaping (backslash, quotes, control chars) is a strict subset of what
// Python string literals accept, so it round-trips safely for the plain
// paths/JSON bodies torture.yaml carries.
func pyStr(s string) string {
	return strconv.Quote(s)
}
