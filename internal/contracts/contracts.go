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
	"os"
	"os/exec"
	"path/filepath"
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
	out, err := exec.Command(OASDiff, "breaking", baseline, specPath).CombinedOutput()
	return interpretExit(OASDiff, out, err)
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
	return interpretExit(Buf, out, err)
}

// interpretExit maps a delegate tool's exit status to an Outcome. Both
// oasdiff breaking and buf breaking document exit code 1 as "breaking
// changes found" and reserve other non-zero codes for their own failures
// (bad arguments, unreadable baseline, config errors) — that convention,
// not this package, is what a "breaking change" result means; this package
// only relays it. This mapping is the one path this project could not run
// against the real binaries (neither is installed here, per the task
// brief) — it is exercised only indirectly, through CheckOpenAPI/CheckProto's
// not-applicable and missing-tool paths, which are.
func interpretExit(tool string, out []byte, err error) Result {
	if err == nil {
		return Result{Tool: tool, Outcome: OutcomePass, Output: string(out)}
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
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
