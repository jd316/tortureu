package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/smoke"
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
	compose := fs.String("compose", "docker-compose.yml", "path to the compose file, used only to find the SUT's container for isolated-network reachability")
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
	if sys, err := detect.Detect(*compose); err != nil {
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
