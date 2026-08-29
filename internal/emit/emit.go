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
// `planned: emit` are implemented. Each covers a distinct, verifiable
// slice and says so in its own output for every verb it does NOT
// translate, rather than silently narrowing or guessing (see each file's
// header for exactly which verbs and why, and what was verified against a
// real binary versus left explicitly unverified).
//
// Emitters register themselves via Register in an init(), rather than
// being listed in a switch here. Three agents extending this package
// concurrently each hit the old hardcoded switch and had to stop rather
// than edit a shared file — that is a design flaw, not three
// inconveniences, so adding an emitter now touches only its own file.
package emit

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/detect"
)

// Emitter renders one delegate-tier tool. sys may be nil: several
// emitters need only torture.yaml, while those that must reach a real
// dependency address (sysbench, memtier, fio) need detection too and must
// say so rather than guessing a host or port.
type Emitter func(cfg *config.Config, sys *detect.System) (string, error)

type entry struct {
	fn Emitter
	// needsSystem records that this emitter cannot work from torture.yaml
	// alone. Callers use it to skip detection — and to skip reporting a
	// detection failure — for the emitters that never consult it.
	needsSystem bool
}

var registry = map[string]entry{}

// Register adds an emitter. Emitters call this from an init() in their own
// file so that adding one never requires editing this one.
func Register(tool string, fn Emitter, needsSystem bool) {
	if _, dup := registry[tool]; dup {
		panic("emit: duplicate emitter registered for " + tool)
	}
	registry[tool] = entry{fn: fn, needsSystem: needsSystem}
}

// NeedsSystem reports whether tool requires a *detect.System. An unknown
// tool needs nothing; Emit reports it as unknown.
func NeedsSystem(tool string) bool {
	return registry[tool].needsSystem
}

// Tools is every tool name Emit accepts, sorted for stable output.
func Tools() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func init() {
	Register("pumba", func(cfg *config.Config, _ *detect.System) (string, error) { return Pumba(cfg) }, false)
	Register("netem", func(cfg *config.Config, _ *detect.System) (string, error) { return Netem(cfg) }, false)
	Register("iptables", func(cfg *config.Config, _ *detect.System) (string, error) { return IPTables(cfg) }, false)
}

// Emit generates the config/command for tool. An unrecognized tool name
// errors listing what is supported — never a silent no-op (R-CLI-8).
func Emit(tool string, cfg *config.Config, sys *detect.System) (string, error) {
	e, ok := registry[tool]
	if !ok {
		return "", fmt.Errorf("tortureu emit: unknown tool %q; supported: %s", tool, strings.Join(Tools(), ", "))
	}
	return e.fn(cfg, sys)
}
