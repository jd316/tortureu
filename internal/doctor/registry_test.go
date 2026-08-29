package doctor_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/detect"
	"github.com/jd316/TortureU/internal/doctor"
)

const registryPath = "../../registry.yaml"

// spec: R-COV-1
func TestLoadRegistryCountsMatchSourceOfTruth(t *testing.T) {
	reg, err := doctor.LoadRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	// registry.yaml is the source of truth (R-COV-1), so derive the expected
	// counts from the parsed file rather than restating them here. The prior
	// version hardcoded 151/152 while its own comment claimed otherwise, and
	// broke on every registry edit — a test that must be updated whenever the
	// data changes is not checking the data, it is duplicating it.
	wantDomains := len(reg.Domains)
	wantTools := 0
	for _, d := range reg.Domains {
		wantTools += len(d.Tools)
	}
	if wantDomains == 0 || wantTools == 0 {
		t.Fatal("registry parsed as empty — nothing to check against")
	}
	if got := reg.DomainCount(); got != wantDomains {
		t.Fatalf("DomainCount() = %d, want %d (domains actually parsed)", got, wantDomains)
	}
	if got := reg.ToolCount(); got != wantTools {
		t.Fatalf("ToolCount() = %d, want %d (tools actually parsed)", got, wantTools)
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
	// Derived from the embedded file itself, for the same reason as above:
	// a hardcoded count here would break on every registry edit without
	// checking anything the parse does not already tell us.
	wantDomains := len(reg.Domains)
	wantTools := 0
	for _, d := range reg.Domains {
		wantTools += len(d.Tools)
	}
	if wantDomains == 0 || wantTools == 0 {
		t.Fatal("embedded registry parsed as empty — the embed is not working")
	}
	if got := reg.DomainCount(); got != wantDomains {
		t.Fatalf("DomainCount() = %d, want %d", got, wantDomains)
	}
	if got := reg.ToolCount(); got != wantTools {
		t.Fatalf("ToolCount() = %d, want %d", got, wantTools)
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

// findTool returns the tool with id across all of reg's domains, failing
// the test if it is not present.
func findTool(t *testing.T, reg *doctor.Registry, id string) doctor.Tool {
	t.Helper()
	for _, d := range reg.Domains {
		for _, tool := range d.Tools {
			if tool.ID == id {
				return tool
			}
		}
	}
	t.Fatalf("no tool %q in registry", id)
	return doctor.Tool{}
}

// spec: R-COV-2
func TestLoadRegistryRoundTripsThePlannedMarker(t *testing.T) {
	reg, err := doctor.LoadRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	// The marker is the only thing that can stop doctor's output from
	// mis-instructing a user to run a verb that exits 2, so the parser must
	// not drop it. Counted from the file rather than pinned to one tool
	// name: this test previously named locust, and silently stopped testing
	// anything the day locust was implemented and its marker cleared.
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := bytes.Count(raw, []byte("planned:"))
	if want == 0 {
		t.Skip("no planned: entries left in registry.yaml — nothing to round-trip")
	}
	got := 0
	for _, d := range reg.Domains {
		for _, tool := range d.Tools {
			if tool.Planned != "" {
				got++
			}
		}
	}
	if got != want {
		t.Fatalf("parsed %d planned markers, registry.yaml has %d", got, want)
	}
}

// spec: R-COV-2
func TestLoadRegistryDoesNotFabricatePlannedForAWorkingVerb(t *testing.T) {
	reg, err := doctor.LoadRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	// k6's how: ("tortureu run <scenario>") already runs, and carries no
	// planned: marker in registry.yaml. The parser must not invent one.
	k6 := findTool(t, reg, "k6")
	if k6.Planned != "" {
		t.Fatalf("k6.Planned = %q, want empty (no planned: marker in registry.yaml)", k6.Planned)
	}
}

// spec: R-SCOPE-4
func TestCoverageEntryStringLabelsPlannedToolsAndDoesNotMisinstruct(t *testing.T) {
	entry := doctor.CoverageEntry{
		Domain:    "load",
		Tool:      doctor.Tool{ID: "locust", Tier: "delegate", When: "lang:python", How: "tortureu emit locust", Planned: "emit"},
		Applies:   true,
		Evaluated: true,
	}
	s := entry.String()
	if !strings.Contains(s, "delegate") {
		t.Fatalf("CoverageEntry.String() = %q, must still show tier %q", s, "delegate")
	}
	if !strings.Contains(s, "planned") {
		t.Fatalf("CoverageEntry.String() = %q, must flag the planned marker so a user isn't told to run a verb that exits 2", s)
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
