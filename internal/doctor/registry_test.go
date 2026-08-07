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

// spec: R-COV-8
func TestLoadEmbeddedRegistryWorksFromAnyWorkingDirectory(t *testing.T) {
	// The bug this guards against: doctor.LoadRegistry("registry.yaml")
	// only works when the process happens to be run from the repo root.
	// A static binary that depends on a file it doesn't ship is not one
	// (R-COV-8) — the embedded loader must work no matter where the
	// binary is invoked from.
	t.Chdir(t.TempDir())

	reg, err := doctor.LoadEmbeddedRegistry()
	if err != nil {
		t.Fatalf("LoadEmbeddedRegistry: %v", err)
	}
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

// spec: R-COV-5
func TestEvalPredicateUsesDetectCoverageFacts(t *testing.T) {
	sys := &detect.System{
		Coverage: detect.Coverage{K8s: true, OpenAPI: false, LacksOtel: detect.FactTrue},
	}

	if m, e := doctor.EvalPredicate("platform:k8s", sys); !e || !m {
		t.Fatalf("platform:k8s: matched=%v evaluated=%v, want true/true", m, e)
	}
	if m, e := doctor.EvalPredicate("spec:openapi", sys); !e || m {
		t.Fatalf("spec:openapi: matched=%v evaluated=%v, want false/true", m, e)
	}
	if m, e := doctor.EvalPredicate("lacks:otel", sys); !e || !m {
		t.Fatalf("lacks:otel: matched=%v evaluated=%v, want true/true", m, e)
	}
}

// spec: R-COV-6
func TestEvalPredicateReportsFactUnknownAsUnevaluable(t *testing.T) {
	// AWS/Azure/LacksOtel are detect.Fact (tri-state): FactUnknown happens
	// when the only manifest present is one R-DET-14 doesn't support.
	// EvalPredicate must surface that as unevaluable, never as a guessed
	// false match.
	sys := &detect.System{
		Coverage: detect.Coverage{AWS: detect.FactUnknown},
	}
	matched, evaluated := doctor.EvalPredicate("platform:aws", sys)
	if evaluated {
		t.Fatalf("expected platform:aws to be unevaluable when detect reports FactUnknown, got matched=%v evaluated=%v", matched, evaluated)
	}
	if matched {
		t.Fatal("an unevaluable predicate must never report a match")
	}
}

// spec: R-COV-4
// spec: R-COV-6
func TestEvalPredicateNeverGuessesUndetectableNamespace(t *testing.T) {
	sys := &detect.System{}
	// has:traffic-capture depends on torture.yaml config, not an R-DET-1
	// input, so detect.System can never answer it (per its own Coverage
	// doc comment). EvalPredicate must say so rather than fabricate an
	// answer — a predicate the system genuinely cannot evaluate must be
	// reported as unevaluable, never silently treated as false (R-COV-6).
	matched, evaluated := doctor.EvalPredicate("has:traffic-capture", sys)
	if evaluated {
		t.Fatalf("expected has:traffic-capture to be unevaluable from detect.System, got matched=%v evaluated=%v", matched, evaluated)
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
