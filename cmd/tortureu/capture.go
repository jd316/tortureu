package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/jdb316/tortureu/internal/detect"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jdb316/tortureu/internal/capture"
)

// runCapture is the `tortureu capture` verb (R-CLI-9): run a small
// scrubbing proxy in front of -upstream and record every exchange to -out
// as a cassette. See internal/capture's package doc for why a
// standalone proxy, not eBPF or a literal keploy integration (design note
// in the task brief; registry.yaml's keploy entry is left `planned` — see
// this task's report for the reasoning).
//
// Scrubbing happens inside internal/capture.Recorder before a single byte
// reaches -out (R-DC2-5): this file only wires flags to that Recorder, it
// contains no capture or scrubbing logic of its own.
func runCapture(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("capture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	upstream := fs.String("upstream", "", "upstream base URL to record traffic to/from (required)")
	listen := fs.String("listen", "127.0.0.1:0", "address the capturing proxy listens on")
	out := fs.String("out", "cassette.jsonl", "path to write the scrubbed cassette (R-DC2-5: scrubbed on write)")
	engine := fs.String("engine", "proxy", "capture engine: \"proxy\" (built-in scrubbing proxy) or \"keploy\" (delegate handoff, R-CLI-12)")
	compose := fs.String("compose", detect.DefaultComposePath, "compose file the -engine keploy handoff is derived from")
	duration := fs.Duration("duration", 0, "stop capturing after this long; 0 runs until interrupted (Ctrl-C)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Engine selection happens before -upstream is required, because
	// -upstream is the proxy engine's input and means nothing to keploy.
	// An unknown engine never falls back (R-CLI-12): a silent fallback
	// would leave the user believing keploy ran and produced eBPF-derived
	// mocks when what ran was our own HTTP proxy.
	switch *engine {
	case "proxy":
	case "keploy":
		// R-DET-15: resolve the compose filename here too. This verb was
		// missed when precedence landed, so it reported "could not detect
		// the system from docker-compose.yml" on a repo using compose.yaml.
		composePath, cerr := detect.ResolveComposeArg(*compose)
		if cerr != nil {
			fmt.Fprintf(stderr, "tortureu capture: %v\n", cerr)
			return 2
		}
		return runCaptureKeploy(composePath, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tortureu capture: -engine %q is not a capture engine; supported: %s\n",
			*engine, strings.Join(captureEngines, ", "))
		return 2
	}

	if *upstream == "" {
		fmt.Fprintln(stderr, "tortureu capture: -upstream is required")
		return 2
	}
	upstreamURL, err := url.Parse(*upstream)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu capture: -upstream: %v\n", err)
		return 2
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu capture: create %s: %v\n", *out, err)
		return 2
	}
	defer f.Close()

	rec := &capture.Recorder{Upstream: upstreamURL, Out: f, ErrOut: stderr}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu capture: listen %s: %v\n", *listen, err)
		return 2
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if *duration > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), *duration)
	} else {
		ctx, cancel = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	}
	defer cancel()

	srv := &http.Server{Handler: rec}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	fmt.Fprintf(stdout, "tortureu capture: listening on %s, forwarding to %s, writing %s\n", ln.Addr(), upstreamURL, *out)

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(stderr, "tortureu capture: serve: %v\n", err)
			return 1
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)

	fmt.Fprintf(stdout, "tortureu capture: stopped, %d exchange(s) captured to %s\n", rec.Count(), *out)
	return 0
}

// captureEngines is the closed set of -engine values (R-CLI-12), listed
// back to the user on an unrecognised one so the answer to "what did I
// mistype" is in the error itself.
var captureEngines = []string{"proxy", "keploy"}

// runCaptureKeploy is the delegate handoff (R-CLI-12): generate keploy's
// command and config for the detected system, print them, run nothing.
// It never guesses keploy's inputs — internal/capture.PlanKeploy refuses,
// and the refusal is the user-facing answer.
func runCaptureKeploy(composePath string, stdout, stderr io.Writer) int {
	plan, err := capture.PlanKeploy(composePath)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	fmt.Fprint(stdout, capture.KeployHandoff(plan))
	return 0
}
