package doctor

import (
	"fmt"
	"os"
	"strings"

	tortureu "github.com/jd316/tortureu"
	"github.com/jd316/tortureu/internal/detect"
	"gopkg.in/yaml.v3"
)

// Tool is one registry.yaml entry (R-COV-2: every entry carries tier, when,
// and how).
//
// Planned carries the verb named in `how:` when that verb is not yet
// implemented (registry.yaml's `planned:` marker, guarded by check.py so no
// entry can name a non-working verb unmarked). It is empty for an entry
// whose `how:` already runs. A parser that silently dropped this would
// leave the marker unreachable by any consumer — R-COV-2 requires the
// Go model to carry an entry's fields faithfully, not just the three it
// names explicitly, precisely so `how:`'s honesty survives past parsing.
type Tool struct {
	ID      string `yaml:"id"`
	Tier    string `yaml:"tier"`
	When    string `yaml:"when"`
	How     string `yaml:"how"`
	Note    string `yaml:"note"`
	Planned string `yaml:"planned"`
}

// Domain groups tools under one capability area.
type Domain struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Tools []Tool `yaml:"tools"`
}

// Registry is the parsed registry.yaml — the source of truth for tool
// coverage (R-COV-1); counts elsewhere must be checked against it.
type Registry struct {
	Version int      `yaml:"version"`
	Domains []Domain `yaml:"domains"`
}

// LoadRegistry reads and parses the registry.yaml at path. Prefer
// LoadEmbeddedRegistry outside tests: this depends on path being reachable
// from the process's current working directory, which is exactly the
// R-COV-8 packaging bug (a binary run from anywhere but the repo root
// couldn't find it).
func LoadRegistry(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("doctor: read registry: %w", err)
	}
	return parseRegistry(raw)
}

// LoadEmbeddedRegistry parses the registry.yaml embedded into the binary
// at build time (R-COV-8), independent of the process's working directory.
// The embedded bytes (registry_embed.go, repo root) come from the same
// registry.yaml file LoadRegistry reads from disk — not a copy — so there
// is nothing for the two to drift against.
func LoadEmbeddedRegistry() (*Registry, error) {
	return parseRegistry(tortureu.RegistryYAML)
}

func parseRegistry(raw []byte) (*Registry, error) {
	var reg Registry
	if err := yaml.Unmarshal(raw, &reg); err != nil {
		return nil, fmt.Errorf("doctor: parse registry: %w", err)
	}
	return &reg, nil
}

// DomainCount is the number of capability domains (R-COV-1).
func (r *Registry) DomainCount() int {
	return len(r.Domains)
}

// ToolCount is the total number of tools across all domains (R-COV-1).
func (r *Registry) ToolCount() int {
	n := 0
	for _, d := range r.Domains {
		n += len(d.Tools)
	}
	return n
}

// CoverageEntry is one registry tool evaluated against a detected system.
// Tool is always carried in full so any rendering of an Entry shows the
// tool's tier — a `know`-tier tool must never be presented as something we
// execute (R-SCOPE-4).
type CoverageEntry struct {
	Domain    string
	Tool      Tool
	Applies   bool // When matched the detected system
	Evaluated bool // whether When could be evaluated at all (R-COV-4)
}

// String renders one coverage line, always labelled with the tool's tier
// (R-SCOPE-4). A tool whose How names a `planned:` (not yet implemented)
// verb is labelled "· planned" next to its tier and its How is annotated,
// so the line never instructs a user to run something that exits 2.
func (c CoverageEntry) String() string {
	status := "applies"
	if !c.Evaluated {
		status = "unknown (undetectable predicate)"
	} else if !c.Applies {
		status = "does not apply"
	}

	tier := c.Tool.Tier
	how := c.Tool.How
	if c.Tool.Planned != "" {
		tier += " · planned"
		how = fmt.Sprintf("%s (verb %q not implemented in v0)", how, c.Tool.Planned)
	}

	return fmt.Sprintf("[%s] %s/%s: %s (%s)", tier, c.Domain, c.Tool.ID, how, status)
}

// Evaluate reports coverage of every registry tool against sys (R-COV-1).
func Evaluate(reg *Registry, sys *detect.System) []CoverageEntry {
	var out []CoverageEntry
	for _, d := range reg.Domains {
		for _, tool := range d.Tools {
			matched, evaluated := EvalPredicate(tool.When, sys)
			out = append(out, CoverageEntry{
				Domain:    d.ID,
				Tool:      tool,
				Applies:   matched,
				Evaluated: evaluated,
			})
		}
	}
	return out
}

// EvalPredicate evaluates a registry.yaml `when:` predicate against sys.
//
// Grammar (registry.yaml header, R-COV-3): `always`, `never`, or one or
// more `namespace:value` terms joined by `|` (OR), each repeating its own
// prefix. Every predicate must be derivable from R-DET-1 inputs alone
// (R-COV-4). R-COV-5 lists the facts detection must expose for
// spec:/platform:/lacks:, which detect.System.Coverage now carries; where a
// fact still has no source (has:traffic-capture needs torture.yaml, not an
// R-DET-1 input), evaluated is false and matched is always false — this
// function never guesses a match for what it cannot evaluate (R-COV-6).
func EvalPredicate(pred string, sys *detect.System) (matched, evaluated bool) {
	switch pred {
	case "always":
		return true, true
	case "never":
		return false, true
	}

	for _, alt := range strings.Split(pred, "|") {
		m, e := evalTerm(alt, sys)
		if !e {
			return false, false
		}
		if m {
			return true, true
		}
	}
	return false, true
}

// coverageFacts maps a full "namespace:value" term to the R-COV-5 fact on
// detect.System.Coverage that answers it. OpenAPI/Proto/K8s are plain bools
// (pure file-presence checks, always determinable); AWS/Azure/LacksOtel are
// detect.Fact, tri-state so an unsupported/absent manifest reports
// FactUnknown rather than a guessed FactFalse (R-COV-6) — factToBool
// carries that unknown through as evaluated=false.
var coverageFacts = map[string]func(detect.Coverage) (matched, evaluated bool){
	"spec:openapi":   func(c detect.Coverage) (bool, bool) { return c.OpenAPI, true },
	"spec:proto":     func(c detect.Coverage) (bool, bool) { return c.Proto, true },
	"platform:k8s":   func(c detect.Coverage) (bool, bool) { return c.K8s, true },
	"platform:aws":   func(c detect.Coverage) (bool, bool) { return factToBool(c.AWS) },
	"platform:azure": func(c detect.Coverage) (bool, bool) { return factToBool(c.Azure) },
	"lacks:otel":     func(c detect.Coverage) (bool, bool) { return factToBool(c.LacksOtel) },
}

// factToBool translates a detect.Fact (R-COV-6 tri-state) into
// EvalPredicate's (matched, evaluated) pair: FactUnknown is never guessed
// as a match.
func factToBool(f detect.Fact) (matched, evaluated bool) {
	switch f {
	case detect.FactTrue:
		return true, true
	case detect.FactFalse:
		return false, true
	default:
		return false, false
	}
}

// evalTerm evaluates a single "namespace:value" predicate term.
func evalTerm(term string, sys *detect.System) (matched, evaluated bool) {
	if fact, ok := coverageFacts[term]; ok {
		return fact(sys.Coverage)
	}

	ns, val, ok := strings.Cut(term, ":")
	if !ok {
		return false, false
	}
	switch ns {
	case "dep":
		for _, d := range sys.Deps {
			if d.Type == val {
				return true, true
			}
		}
		return false, true
	case "lang":
		return sys.Lang == val, true
	default:
		// has:traffic-capture (a config fact, not an R-DET-1 input) and
		// anything else outside R-COV-5's table are not derivable from
		// detect.System. Report the gap rather than fabricate a match
		// (R-COV-6).
		return false, false
	}
}
