// Command tortureu is the single front door to TortureU's 151 tools across
// 19 capability domains (R-SCOPE-3): one CLI, three tool depths (drive,
// delegate, know). See PLAN.md "Task 8" and SPEC.md §7 (R-CLI-1..3).
package main

import (
	"fmt"
	"io"
	"os"
)

// stubVerbs are declared in registry.yaml (R-CLI-2) but not implemented in
// v0 (PLAN.md Task 8): they exit 2 naming themselves rather than silently
// doing nothing or pretending to succeed.
var stubVerbs = map[string]bool{
	"emit":    true,
	"capture": true,
	"replay":  true,
}

func main() {
	os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
}

// Main dispatches one CLI invocation over the nine v0 verbs (R-CLI-1). It
// never calls os.Exit itself so it can be driven directly by tests.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	verb, rest := args[0], args[1:]
	switch {
	case verb == "init":
		return runInit(rest, stdout, stderr)
	case verb == "run":
		return runRun(rest, stdout, stderr)
	case verb == "smoke":
		return runSmoke(rest, stdout, stderr)
	case verb == "doctor":
		return runDoctor(rest, stdout, stderr)
	case verb == "mcp":
		return runMcp(os.Stdin, rest, stdout, stderr)
	case verb == "check":
		return runCheck(rest, stdout, stderr)
	case stubVerbs[verb]:
		fmt.Fprintf(stderr, "tortureu %s: not implemented in v0\n", verb)
		return 2
	default:
		fmt.Fprintf(stderr, "tortureu: unknown verb %q\n\n%s", verb, usage)
		return 2
	}
}
