package doctor_test

import (
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/doctor"
)

const registryPath = "../../registry.yaml"

// spec: R-COV-1
func TestLoadRegistryCountsMatchSourceOfTruth(t *testing.T) {
	reg, err := doctor.LoadRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	// registry.yaml is the source of truth (R-COV-1): these counts are
	// derived from the file, not restated by hand.
	if got := reg.DomainCount(); got != 19 {
		t.Fatalf("DomainCount() = %d, want 19", got)
	}
	if got := reg.ToolCount(); got != 151 {
		t.Fatalf("ToolCount() = %d, want 151", got)
	}
}

// spec: R-COV-2
func TestLoadRegistryEveryToolHasTierWhenHow(t *testing.T) {
	reg, err := doctor.LoadRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	for _, d := range reg.Domains {
		for _, tool := range d.Tools {
			if tool.Tier == "" || tool.When == "" || tool.How == "" {
				t.Fatalf("tool %q in domain %q missing tier/when/how: %+v", tool.ID, d.ID, tool)
			}
		}
	}
}

// spec: R-COV-3
func TestEvalPredicateOrAlternativesRepeatPrefix(t *testing.T) {
	sys := &detect.System{
		Deps: []detect.Dep{{Name: "queue", Type: "sqs"}},
	}
	matched, evaluated := doctor.EvalPredicate("dep:kafka|dep:sqs", sys)
	if !evaluated {
		t.Fatal("expected dep: predicate to be evaluable")
	}
	if !matched {
		t.Fatal("expected dep:kafka|dep:sqs to match a detected sqs dependency")
	}
}

// spec: R-COV-4
func TestEvalPredicateNeverGuessesUndetectableNamespace(t *testing.T) {
	sys := &detect.System{}
	// platform:/spec:/has:/lacks: are not derivable from detect.System as it
	// exists today (R-DET-1 gives us compose + manifests, not platform or
	// spec info). EvalPredicate must say so rather than fabricate an answer.
	matched, evaluated := doctor.EvalPredicate("platform:k8s", sys)
	if evaluated {
		t.Fatalf("expected platform: predicate to be unevaluable from detect.System, got matched=%v evaluated=%v", matched, evaluated)
	}
	if matched {
		t.Fatal("an unevaluable predicate must never report a match")
	}
}

// spec: R-SCOPE-4
func TestCoverageEntryStringShowsTier(t *testing.T) {
	entry := doctor.CoverageEntry{
		Domain:    "load",
		Tool:      doctor.Tool{ID: "wrk2", Tier: "know", When: "never", How: "wrk2 -R<rate>"},
		Applies:   false,
		Evaluated: true,
	}
	s := entry.String()
	if !strings.Contains(s, "know") {
		t.Fatalf("CoverageEntry.String() = %q, must show tier %q", s, "know")
	}
}
