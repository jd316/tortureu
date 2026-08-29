package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/smoke"
)

// runSmoke is the `tortureu smoke` verb (R-CLI-6, proposed): a
// constant-rate sanity check that needs no torture.yaml (SPEC.md does not
// yet describe this verb; see this task's report for the proposed
// requirement text).
//
// Rate and duration default low (5 req/s for 10s): this answers "is the
// stack alive", not a load test — someone reaching for actual load belongs
// at `tortureu run` with a k6-driven scenario.
func runSmoke(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("smoke", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "", "base URL to hit (required) — smoke has no torture.yaml to read a target from")
	compose := fs.String("compose", detect.DefaultComposePath, "path to the compose file, used only to find the SUT's container for isolated-network reachability")
	rate := fs.Float64("rate", 5, "requests per second")
	duration := fs.Duration("duration", 10*time.Second, "total time to drive traffic")
	timeout := fs.Duration("timeout", 3*time.Second, "per-request timeout")
	minSuccessRate := fs.Float64("min-success-rate", 1.0, "minimum fraction of requests that must succeed to pass (R-VER-7 exit 0 vs 1)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *url == "" {
		fmt.Fprintln(stderr, "tortureu smoke: -url is required")
		return 2
	}

	// detect.Detect finds the SUT's compose service name so smoke can
	// reach it even when it sits on a DC-2 internal-only network (see
	// internal/smoke/reach.go). Detection failure is a warning, not fatal:
	// smoke's direct-dial fast path works against any ordinarily-reachable
	// URL with no compose file at all.
	sutService := ""
	// R-DET-15: an unset -compose resolves by the Compose Specification's
	// own precedence, so a repo using compose.yaml (the canonical name, and
	// what nearly every real project uses) works without a flag.
	// Resolution failure is not fatal here for the same reason detection
	// failure is not: smoke's direct-dial fast path needs no compose file
	// at all. Falling back to the raw flag keeps the "no compose file"
	// message coming from detect, as before.
	composePath := *compose
	if resolved, cerr := detect.ResolveComposeArg(*compose); cerr == nil {
		composePath = resolved
	}

	if sys, err := detect.Detect(composePath); err != nil {
		fmt.Fprintf(stderr, "tortureu smoke: detect: %v (continuing without isolated-network reachability)\n", err)
	} else {
		sutService = sys.SUT
	}

	client := smoke.NewClient(sutService, *timeout)
	defer client.Close()

	res := smoke.Run(client.Client, *url, smoke.Options{
		Rate:     *rate,
		Duration: *duration,
		Timeout:  *timeout,
	})
	fmt.Fprint(stdout, smoke.Render(*url, res))
	return smoke.ExitCode(res, *minSuccessRate)
}
