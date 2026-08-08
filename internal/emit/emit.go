// Package emit generates delegate-tier tool config/commands from a parsed
// torture.yaml (SPEC.md proposed R-CLI-8; the `emit` verb, R-CLI-1).
//
// "delegate" (registry.yaml header) means: we generate its config/command
// and hand off; real output, separate timing. That last clause matters for
// every tool in this package: none of them schedule against a fault's at:
// phase anchor the way `run` does against k6's clock (R-EXE-8). A
// delegate-tier command has no k6 process to anchor against, so `at:` is
// surfaced as a comment for the human running the command, never
// automated. This is a scope line, not an oversight.
//
// Only a defensible subset of the ~30 registry.yaml tools marked
// `planned: emit` are implemented here: pumba, netem (raw tc), and
// iptables — the three `faults:` network/container translators the task
// prioritized highest after load generators. Each covers a distinct,
// verifiable slice of the fault-verb space and says so in its own output
// for every verb it does NOT translate, rather than silently narrowing or
// guessing (see each file's header for exactly which verbs and why).
package emit

import (
	"fmt"
	"strings"

	"github.com/jdb316/tortureu/internal/config"
)

// Tools is every tool name Emit accepts, in the order they were built.
var Tools = []string{"pumba", "netem", "iptables"}

// Emit generates the config/command for tool from cfg. An unrecognized
// tool name errors with the list Tools names — never a silent no-op
// (task instruction).
func Emit(tool string, cfg *config.Config) (string, error) {
	switch tool {
	case "pumba":
		return Pumba(cfg)
	case "netem":
		return Netem(cfg)
	case "iptables":
		return IPTables(cfg)
	default:
		return "", fmt.Errorf("tortureu emit: unknown tool %q; supported: %s", tool, strings.Join(Tools, ", "))
	}
}
