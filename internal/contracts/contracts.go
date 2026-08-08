// Package contracts implements `tortureu check contracts` (R-CLI-7): the
// delegate-tier breaking-change detectors registry.yaml names as oasdiff
// (spec:openapi) and buf-breaking (spec:proto).
//
// Both are delegate tier (R-SCOPE-4): this package does not reimplement
// oasdiff or buf. It detects what applies (via internal/detect's R-COV-5
// Coverage, computed once and passed in — never re-walked here) and, if the
// real tool is present, hands off to it. If the tool is absent, it says so
// with an install hint, the same shape doctor's prerequisite preflight uses
// for k6/docker (R-CLI-5).
package contracts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Tool names as they appear on PATH.
const (
	OASDiff = "oasdiff"
	Buf     = "buf"
)

// InstallHints are shown when a delegate tool named here is absent
// (R-CLI-5 pattern): tortureu never guesses an install location, only
// states how to get one.
var InstallHints = map[string]string{
	OASDiff: "install: https://github.com/oasdiff/oasdiff#installation",
	Buf:     "install: https://buf.build/docs/installation",
}

// openapiFilenames are the conventional OpenAPI/Swagger document names.
// This mirrors internal/detect's own (unexported) table because that fact
// is a plain bool (R-COV-5 spec:openapi) with no path attached — locating
// the actual file to hand oasdiff is a separate concern from detecting
// that one exists, and internal/detect is explicitly not to be changed to
// expose it.
var openapiFilenames = []string{
	"openapi.yaml", "openapi.yml", "openapi.json",
	"swagger.yaml", "swagger.yml", "swagger.json",
}

// Outcome is what running (or not running) one delegate contract check
// produced.
type Outcome int

const (
	// OutcomeNotApplicable means internal/detect's Coverage did not find
	// this spec type at all, so there was nothing to delegate to.
	OutcomeNotApplicable Outcome = iota
	// OutcomeMissingTool means the spec type was detected but the tool
	// that checks it isn't on PATH.
	OutcomeMissingTool
	// OutcomePass means the tool ran and reported no breaking changes.
	OutcomePass
	// OutcomeBreaking means the tool ran and reported a breaking change.
	// This is a result (R-VER-2's fail, not error), not a tool failure.
	OutcomeBreaking
	// OutcomeError means the tool ran but failed for a reason other than
	// reporting a breaking change (bad baseline, config error, etc.).
	OutcomeError
)

// Result is the outcome of one delegate contract check.
type Result struct {
	Tool    string
	Outcome Outcome
	Hint    string // set only when Outcome == OutcomeMissingTool
	Output  string // combined stdout+stderr, for human display
	Err     error  // set only when Outcome == OutcomeError
}

// CheckOpenAPI runs `oasdiff breaking <baseline> <specPath>` when detected
// is true (R-COV-5 spec:openapi) and oasdiff is on PATH. detected is
// internal/detect's already-computed Coverage.OpenAPI fact — this function
// never re-walks the filesystem to decide whether OpenAPI applies.
func CheckOpenAPI(detected bool, baseline, specPath string) Result {
	if !detected {
		return Result{Tool: OASDiff, Outcome: OutcomeNotApplicable}
	}
	if _, err := exec.LookPath(OASDiff); err != nil {
		return Result{Tool: OASDiff, Outcome: OutcomeMissingTool, Hint: InstallHints[OASDiff]}
	}
	// --fail-on ERR is load-bearing, not a preference: `oasdiff breaking`
	// prints its findings and still exits 0, so without it every breaking
	// change reads as a pass — a silent false negative in the one check
	// whose whole job is to catch them.
	out, err := exec.Command(OASDiff, "breaking", baseline, specPath, "--fail-on", "ERR").CombinedOutput()
	return interpretExit(OASDiff, oasdiffBreakingExit, out, err)
}

// CheckProto runs `buf breaking <dir> --against <baseline>` when detected
// is true (R-COV-5 spec:proto) and buf is on PATH. detected is
// internal/detect's already-computed Coverage.Proto fact.
func CheckProto(detected bool, baseline, dir string) Result {
	if !detected {
		return Result{Tool: Buf, Outcome: OutcomeNotApplicable}
	}
	if _, err := exec.LookPath(Buf); err != nil {
		return Result{Tool: Buf, Outcome: OutcomeMissingTool, Hint: InstallHints[Buf]}
	}
	out, err := exec.Command(Buf, "breaking", dir, "--against", baseline).CombinedOutput()
	return interpretExit(Buf, bufBreakingExit, out, err)
}

// The exit status each delegate uses for "breaking changes found". They do
// not agree, and neither matches the 1 this package originally assumed:
// measured against oasdiff (main) and buf 1.72.0, buf exits 100 and oasdiff
// exits 1 only under --fail-on ERR. Guessing here is not harmless — mapping
// buf's 100 to "tool error" reports a real finding as our own failure, which
// R-VER-2 forbids precisely because it sends the user to debug TortureU
// instead of their schema.
const (
	oasdiffBreakingExit = 1
	bufBreakingExit     = 100
)

// interpretExit maps a delegate tool's exit status to an Outcome, where
// breakingExit is that tool's own code for "breaking changes found" and any
// other non-zero status is the tool failing (bad arguments, unreadable
// baseline, config errors). That convention, not this package, is what a
// breaking change means; this package only relays it.
func interpretExit(tool string, breakingExit int, out []byte, err error) Result {
	if err == nil {
		return Result{Tool: tool, Outcome: OutcomePass, Output: string(out)}
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == breakingExit {
		return Result{Tool: tool, Outcome: OutcomeBreaking, Output: string(out)}
	}
	return Result{Tool: tool, Outcome: OutcomeError, Output: string(out), Err: err}
}

// FindOpenAPISpec locates a conventionally-named OpenAPI/Swagger document
// directly inside dir, returning an error if none of the known names is
// present. It is a plain, single-directory file-presence check — not a
// tree walk — used only to hand CheckOpenAPI a concrete path once
// Coverage.OpenAPI has already said one exists somewhere.
func FindOpenAPISpec(dir string) (string, error) {
	for _, name := range openapiFilenames {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", &os.PathError{Op: "find", Path: dir, Err: os.ErrNotExist}
}

// Available reports whether a delegate tool is on PATH. Callers use it to
// order their work: resolving a baseline before knowing the tool exists
// turns "oasdiff is not installed" — which tells the user exactly what to
// do — into whatever the resolution happened to fail on first.
func Available(tool string) bool {
	_, err := exec.LookPath(tool)
	return err == nil
}

// ResolveOpenAPIBaseline turns R-CLI-7's "-baseline is a git ref or file
// path" into something oasdiff can actually open. An existing path is used
// unchanged; anything else is treated as a git ref and materialised with
// `git show <ref>:<path>` into a temp file, because a ref names content that
// exists only in history — handing it to oasdiff as a filename is how this
// half of the flag came to fail with "open HEAD: no such file or directory".
//
// The returned cleanup removes the temp file and is never nil.
//
// An unresolvable ref is an error naming the ref. It must not degrade into
// comparing the spec against itself, which would report "no breaking
// changes" no matter what the user actually changed.
func ResolveOpenAPIBaseline(baseline, specPath string) (string, func(), error) {
	noop := func() {}
	if _, err := os.Stat(baseline); err == nil {
		return baseline, noop, nil
	}

	dir := filepath.Dir(specPath)
	rel, err := gitRelPath(dir, specPath)
	if err != nil {
		return "", noop, err
	}
	cmd := exec.Command("git", "show", baseline+":"+rel)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", noop, fmt.Errorf("baseline %q is neither an existing file nor a resolvable git ref for %s: %w", baseline, rel, err)
	}

	f, err := os.CreateTemp("", "tortureu-baseline-*"+filepath.Ext(specPath))
	if err != nil {
		return "", noop, err
	}
	cleanup := func() { os.Remove(f.Name()) }
	if _, err := f.Write(out); err != nil {
		f.Close()
		cleanup()
		return "", noop, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", noop, err
	}
	return f.Name(), cleanup, nil
}

// ResolveProtoBaseline is the same contract for buf, which needs no temp
// file: buf reads a git baseline natively through its own input syntax
// (".git#ref=<ref>,subdir=<dir>"). A bare ref does not work — buf exits 1
// with "had no .proto files", which is a failed baseline, not a finding.
func ResolveProtoBaseline(baseline, protoDir string) (string, error) {
	if _, err := os.Stat(baseline); err == nil {
		return baseline, nil
	}
	root, err := gitRoot(protoDir)
	if err != nil {
		return "", fmt.Errorf("baseline %q is neither an existing path nor usable as a git ref: %w", baseline, err)
	}
	spec := filepath.Join(root, ".git") + "#ref=" + baseline
	if rel, err := filepath.Rel(root, protoDir); err == nil && rel != "." {
		spec += ",subdir=" + filepath.ToSlash(rel)
	}
	return spec, nil
}

// gitRoot returns the working-tree root of the repo containing dir.
func gitRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitRelPath expresses path relative to its repo root, which is the form
// `git show <ref>:<path>` requires.
func gitRelPath(dir, path string) (string, error) {
	root, err := gitRoot(dir)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
