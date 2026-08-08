package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/jdb316/tortureu/internal/contracts"
	"github.com/jdb316/tortureu/internal/detect"
)

// runCheck is the `tortureu check` verb: currently just contract
// breaking-change detection (R-CLI-7), the only `check` subcommand
// registry.yaml names (oasdiff, buf-breaking).
func runCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "tortureu check: missing subcommand (want \"contracts\")")
		return 2
	}
	switch args[0] {
	case "contracts":
		return runCheckContracts(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tortureu check: unrecognized subcommand %q (want \"contracts\")\n", args[0])
		return 2
	}
}

// runCheckContracts implements `tortureu check contracts` (R-CLI-7):
// detect which specs exist (internal/detect's R-COV-5 Coverage, computed
// once and never re-walked here) and, for each one present, delegate to
// the real breaking-change tool — oasdiff for spec:openapi, buf breaking
// for spec:proto (both `delegate` tier in registry.yaml: this project does
// not reimplement either).
func runCheckContracts(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check contracts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	compose := fs.String("compose", detect.DefaultComposePath, "path to the compose file to detect")
	baseline := fs.String("baseline", "", "baseline to compare against: a git ref (e.g. main) or a file path (required — tortureu does not guess a baseline)")
	openapiSpec := fs.String("openapi-spec", "", "path to the current OpenAPI document; auto-detected by conventional filename next to the compose file if empty")
	protoDir := fs.String("proto-dir", "", "directory buf breaking should check; defaults to the compose file's directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *baseline == "" {
		fmt.Fprintln(stderr, "tortureu check contracts: --baseline is required (a git ref or file path); refusing to guess one")
		return 2
	}

	// R-DET-15: an unset -compose resolves by the Compose Specification's
	// own precedence, so a repo using compose.yaml (the canonical name, and
	// what nearly every real project uses) works without a flag.
	composePath, cerr := detect.ResolveComposeArg(*compose)
	if cerr != nil {
		fmt.Fprintf(stderr, "tortureu check contracts: %v\n", cerr)
		return 2
	}

	sys, err := detect.Detect(composePath)
	if err != nil {
		fmt.Fprintf(stderr, "tortureu check contracts: detect: %v\n", err)
		return 2
	}
	dir := filepath.Dir(*compose)

	var results []contracts.Result

	if sys.Coverage.OpenAPI {
		spec := *openapiSpec
		if spec == "" {
			spec, err = contracts.FindOpenAPISpec(dir)
			if err != nil {
				fmt.Fprintf(stderr, "tortureu check contracts: spec:openapi detected but no conventional OpenAPI file found in %s (pass -openapi-spec): %v\n", dir, err)
				return 2
			}
		}
		base := *baseline
		if contracts.Available(contracts.OASDiff) {
			resolved, cleanup, rerr := contracts.ResolveOpenAPIBaseline(base, spec)
			if rerr != nil {
				fmt.Fprintf(stderr, "tortureu check contracts: %v\n", rerr)
				return 2
			}
			defer cleanup()
			base = resolved
		}
		results = append(results, contracts.CheckOpenAPI(true, base, spec))
	} else {
		results = append(results, contracts.CheckOpenAPI(false, "", ""))
	}

	pd := dir
	if *protoDir != "" {
		pd = *protoDir
	}
	if sys.Coverage.Proto {
		base := *baseline
		if contracts.Available(contracts.Buf) {
			resolved, rerr := contracts.ResolveProtoBaseline(base, pd)
			if rerr != nil {
				fmt.Fprintf(stderr, "tortureu check contracts: %v\n", rerr)
				return 2
			}
			base = resolved
		}
		results = append(results, contracts.CheckProto(true, base, pd))
	} else {
		results = append(results, contracts.CheckProto(false, "", ""))
	}

	return renderCheckResults(results, stdout, stderr)
}

// renderCheckResults prints each delegate check's outcome and derives the
// overall exit code (R-VER-7's meanings, applied to `check` rather than
// `run`): a breaking change found is a result (fail, 1), not a tool error
// — the same status/error distinction R-VER-2 draws for `run` — but a
// required tool being absent, or failing to run for a reason other than
// reporting breakage, is an error (2): tortureu could not do the check it
// was asked to do. Error takes precedence over a breaking-change result
// because an errored check's "no breaking change reported" can't be
// trusted the way a check that actually ran can.
func renderCheckResults(results []contracts.Result, stdout, stderr io.Writer) int {
	any := false
	worst := 0
	for _, r := range results {
		switch r.Outcome {
		case contracts.OutcomeNotApplicable:
			continue
		case contracts.OutcomeMissingTool:
			any = true
			fmt.Fprintf(stdout, "[error] %s: not found on PATH — %s\n", r.Tool, r.Hint)
			worst = max(worst, 2)
		case contracts.OutcomePass:
			any = true
			fmt.Fprintf(stdout, "[ok] %s: no breaking changes\n", r.Tool)
		case contracts.OutcomeBreaking:
			any = true
			fmt.Fprintf(stdout, "[fail] %s: breaking change detected\n", r.Tool)
			if r.Output != "" {
				fmt.Fprint(stdout, r.Output)
			}
			worst = max(worst, 1)
		case contracts.OutcomeError:
			any = true
			fmt.Fprintf(stdout, "[error] %s: %v\n", r.Tool, r.Err)
			if r.Output != "" {
				fmt.Fprint(stdout, r.Output)
			}
			worst = max(worst, 2)
		}
	}
	if !any {
		fmt.Fprintln(stdout, "check contracts: no spec:openapi or spec:proto detected — nothing to check")
		return 0
	}
	return worst
}
