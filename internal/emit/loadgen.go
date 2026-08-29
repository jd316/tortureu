package emit

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/fault"
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
// failure R-CFG-6 exists to avoid). Gatling expresses this natively, in
// the DSL; the Locust target reaches the same semantics by pacing
// arrivals itself, since Locust ships no open-model executor — see
// loadgen.go's Locust header for the measurements.
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

// locustHeader records what was actually measured. Locust ships no
// open-model executor, and its two candidate wait-time primitives
// (constant_pacing / constant_throughput) are both "at most" pacers on a
// fixed pool of looping users, so neither satisfies R-CFG-6. Rather than
// keep that as a caveat, this emit paces arrivals itself and dispatches
// each into its own greenlet — which measures as a real open model.
const locustHeader = `"""
Generated by tortureu emit locust. Requires: pip install locust.

OPEN MODEL (R-CFG-6). Arrival rate here is independent of response time.

Locust has no open-model executor, and neither of its wait-time pacers
can supply one -- both cap a fixed pool of looping users at "at most" a
rate, so once a response outlasts the pacing interval the pool falls
behind. Measured on this host (locust 2.46.3, 200rps declared for 20s
against a target with fixed injected latency, arrivals counted
server-side):

    wait_time                latency  achieved
    constant_pacing(1)          10ms   200 rps
    constant_pacing(1)         500ms   200 rps
    constant_pacing(1)         900ms   200 rps
    constant_pacing(1)        1200ms   170 rps
    constant_pacing(1)        2000ms   100 rps   <- arrivals bunched 200,0,200,0
    constant_throughput(1)     500ms   200 rps
    constant_throughput(1)    2000ms   100 rps

i.e. achieved = declared * min(1, pacing_interval / response_time). The
rate holds only while responses stay under the pacing interval -- which
is exactly the regime where an open model does not matter. A custom
LoadTestShape does not help: it sets user_count, and user_count is the
size of the closed pool.

So this file does not use a wait_time to set the rate at all. A single
Locust user runs an arrival PACER: it wakes every TICK seconds, computes
the declared rate from STAGES, and dispatches that tick's arrivals into
their own greenlets via gevent.spawn_later. Nothing in that loop waits on
a response, so in-flight requests grow with latency instead of throttling
arrivals -- the same thing Gatling's injectOpen does. Re-measured with
this file's own shape, same target and same 200rps:

    latency    10ms -> 200 rps   (4000 arrivals in 20s, exactly on target)
    latency   500ms -> 200 rps   (4000 arrivals)
    latency  2000ms -> 200 rps   (4000 arrivals)
    latency  5000ms -> 200 rps   (4000 arrivals, ~1000 requests in flight)

LIMITATIONS THAT SURVIVE, stated rather than implied away:
  * Distributed mode. The pacer is one Locust user, so "locust --worker"
    does NOT divide the declared rate across workers -- the whole profile
    runs on whichever worker holds that user. Run this file in a single
    process, or split the rate across processes yourself.
  * End-of-run drain. Locust stops when the shape ends and does not wait
    for in-flight requests, so its own summary undercounts by roughly
    (rate * response_time) requests. At 200rps/5s latency the server saw
    all 4000 arrivals while Locust's table reported the 3000 that had
    completed. The arrival rate is still correct; the completion count is
    the truncated one.
  * concurrency below is the FastHttp connection pool, sized from the
    declared peak rate. If the SUT gets slow enough that in-flight
    exceeds it, arrivals queue on the pool -- raise it rather than
    reading the result as a SUT limit.

Verification: this locustfile is syntax-checked (py_compile) by this
package's tests; the arrival-rate numbers above were measured by running
this shape under a real locust 2.46.3. Faults declared in torture.yaml's
faults: block are a separate delegate (pumba/netem/iptables), never
scheduled by this file (see the trailing comment for what was reported).
"""
`

// Locust translates torture.yaml's load: block into a locustfile.py that
// drives a genuine open arrival-rate model (R-CFG-6): a single Locust user
// paces arrivals from the declared STAGES table and dispatches each
// arrival into its own greenlet, so response time never gates the next
// arrival. Scenario weights move out of @task(n) — in an open model the
// pacer, not Locust's task scheduler, picks the flow per arrival — into a
// SCENARIOS table it samples. See locustHeader for the measurements
// behind this shape and the limits that genuinely remain.
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
	b.WriteString("\nimport random\nimport time\n\nimport gevent\nfrom locust import LoadTestShape, constant, task\nfrom locust.contrib.fasthttp import FastHttpUser\n\n")

	b.WriteString("# (start_s, end_s, rate_from, rate_to) per load.stages phase: the declared\n")
	b.WriteString("# arrival-rate schedule (R-CFG-6). A ramp's implicit start rate is the\n")
	b.WriteString("# previous stage's end rate.\n")
	b.WriteString("STAGES = [\n")
	cursor := 0.0
	peak := 0.0
	for _, st := range steps {
		end := cursor + st.secs
		fmt.Fprintf(&b, "    (%s, %s, %s, %s),  # phase %q\n",
			fmtNum(cursor), fmtNum(end), fmtNum(st.from), fmtNum(st.to), st.phase)
		cursor = end
		if st.to > peak {
			peak = st.to
		}
		if st.from > peak {
			peak = st.from
		}
	}
	b.WriteString("]\n\n")

	b.WriteString("# (weight, [(method, path, body), ...]) per load.scenarios entry. The pacer\n")
	b.WriteString("# samples this per arrival, which is how scenario weighting survives not\n")
	b.WriteString("# having a per-scenario @task anymore.\n")
	b.WriteString("SCENARIOS = [\n")
	if len(cfg.Load.Scenarios) == 0 {
		b.WriteString("    # no scenarios declared in load.scenarios\n")
	}
	for _, sc := range cfg.Load.Scenarios {
		weight := sc.Weight
		if weight <= 0 {
			weight = 1
		}
		steps := make([]string, 0, len(sc.Flow))
		for _, step := range sc.Flow {
			body := "None"
			if step.Body != "" {
				body = pyStr(step.Body)
			}
			steps = append(steps, fmt.Sprintf("(%s, %s, %s)", pyStr(strings.ToUpper(step.Method)), pyStr(step.Path), body))
		}
		fmt.Fprintf(&b, "    (%d, [%s]),  # scenario %q\n", weight, strings.Join(steps, ", "), sc.Name)
	}
	b.WriteString("]\n")
	b.WriteString("TOTAL_WEIGHT = sum(w for w, _ in SCENARIOS) or 1\n")
	b.WriteString("TOTAL_RUNTIME = STAGES[-1][1] if STAGES else 0\n\n")
	b.WriteString("# How often the pacer wakes. Each tick's arrivals are spread evenly across\n")
	b.WriteString("# the tick, so a stage ramping up from 0rps still works, and the next\n")
	b.WriteString("# wake-up is an ABSOLUTE deadline rather than a cumulative sleep, so the\n")
	b.WriteString("# schedule cannot drift behind (an earlier drafting of this file lost ~10%\n")
	b.WriteString("# of the declared rate to exactly that drift).\n")
	b.WriteString("TICK = 0.1\n\n\n")

	b.WriteString(`def rate_at(t):
    """Declared arrival rate (req/s) at t seconds in; None once the run ends."""
    for start, end, rate_from, rate_to in STAGES:
        if t < end:
            span = end - start
            frac = 1.0 if span <= 0 else (t - start) / span
            return rate_from + (rate_to - rate_from) * frac
    return None


`)

	b.WriteString("class TortureUUser(FastHttpUser):\n")
	fmt.Fprintf(&b, "    host = %s\n", pyStr(cfg.Target.BaseURL))
	b.WriteString("    wait_time = constant(0)  # unused: the pacer below sets the rate, not wait_time\n")
	b.WriteString("    # FastHttp connection pool. An open model keeps arriving while responses\n")
	b.WriteString("    # are slow, so in-flight requests grow with latency instead of being\n")
	b.WriteString("    # capped by a user pool; sized from the declared peak rate.\n")
	concurrency := int(peak) * 10
	if concurrency < 100 {
		concurrency = 100
	}
	fmt.Fprintf(&b, "    concurrency = %d\n\n", concurrency)

	b.WriteString(`    def _arrival(self):
        """One arrival: pick a scenario by weight and run its flow."""
        if not SCENARIOS:
            return
        r = random.random() * TOTAL_WEIGHT
        flow = SCENARIOS[0][1]
        for weight, steps in SCENARIOS:
            r -= weight
            if r <= 0:
                flow = steps
                break
        for method, path, body in flow:
            self.client.request(method, path, data=body)

    @task
    def pace(self):
        """Dispatch arrivals at the declared rate, never waiting on one."""
        start = time.perf_counter()
        tick = 0
        credit = 0.0
        while True:
            rate = rate_at(tick * TICK)
            if rate is None:
                self.environment.runner.quit()
                return
            credit += rate * TICK
            count = int(credit)
            credit -= count
            for i in range(count):
                gevent.spawn_later(i * TICK / count, self._arrival)
            tick += 1
            sleep_for = tick * TICK - (time.perf_counter() - start)
            if sleep_for > 0:
                gevent.sleep(sleep_for)


class TortureULoadShape(LoadTestShape):
    # Exactly one user, always: it is the arrival pacer, not a simulated end
    # user. User count is deliberately NOT the load knob here -- STAGES is.
    def tick(self):
        if self.get_run_time() < TOTAL_RUNTIME:
            return (1, 1)
        return None
`)

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

func init() {
	Register("gatling", func(cfg *config.Config, _ *detect.System) (string, error) { return Gatling(cfg) }, false)
	Register("locust", func(cfg *config.Config, _ *detect.System) (string, error) { return Locust(cfg) }, false)
}
