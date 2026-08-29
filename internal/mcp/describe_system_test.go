package mcp

import (
	"testing"

	"github.com/jd316/tortureu/internal/detect"
)

// spec: R-DET-7
func TestDescribeSystem_ReturnsGapsExplicitly(t *testing.T) {
	sys := &detect.System{
		SUT: "checkout-api",
		Deps: []detect.Dep{
			{Name: "postgres", Type: "postgresql", Address: "postgres:5432", Clients: []string{"jackc/pgx"}},
		},
		Egress: []string{"postgres:5432", "api.partner.com"},
		EgressClass: map[string]string{
			"postgres:5432":   "internal",
			"api.partner.com": "unclassified",
		},
		Obs:  detect.Obs{Traces: false, Metrics: true, MaxConfidence: "correlated"},
		Gaps: []string{"api.partner.com reached from source we did not parse — classify it in torture.yaml"},
	}

	out := DescribeSystem(sys)

	if len(out.Gaps) != 1 || out.Gaps[0] != sys.Gaps[0] {
		t.Fatalf("Gaps = %v, want detect.System's gaps carried through verbatim (R-DET-7)", out.Gaps)
	}
	if len(out.Deps) != 1 || out.Deps[0].Name != "postgres" || out.Deps[0].Type != "postgresql" {
		t.Fatalf("Deps = %+v, want the detected postgres dependency", out.Deps)
	}
	if len(out.Egress.Unclassified) != 1 || out.Egress.Unclassified[0] != "api.partner.com" {
		t.Errorf("Egress.Unclassified = %v, want [api.partner.com]", out.Egress.Unclassified)
	}
	if len(out.Egress.Classified) != 1 || out.Egress.Classified[0] != "postgres:5432" {
		t.Errorf("Egress.Classified = %v, want [postgres:5432]", out.Egress.Classified)
	}
}

// spec: R-DET-7
func TestDescribeSystem_NoGapsIsAnEmptySliceNotNil(t *testing.T) {
	sys := &detect.System{SUT: "checkout-api"}
	out := DescribeSystem(sys)
	if out.Gaps == nil {
		t.Error("Gaps is nil when there are none — silence about \"no gaps\" must still be an explicit empty list, not the zero value of an absent field (R-DET-7)")
	}
}

// spec: R-MCP-6
func TestDescribeSystem_IncludesRegistrySuggestionsForDetectedSystem(t *testing.T) {
	sys := &detect.System{
		SUT:  "checkout-api",
		Deps: []detect.Dep{{Name: "postgres", Type: "postgresql", Address: "postgres:5432"}},
	}

	out := DescribeSystem(sys)

	found := false
	for _, s := range out.Suggestions {
		// hammerdb (registry.yaml, tier: know, when: dep:postgresql|...) is
		// the delegate/know-tier reach R-MCP-6 exists to give an agent:
		// nothing in the five MCP tools alone would ever surface it.
		if s.ID == "hammerdb" {
			found = true
			if s.Tier != "know" {
				t.Errorf("hammerdb suggestion Tier = %q, want %q", s.Tier, "know")
			}
		}
	}
	if !found {
		t.Fatalf("Suggestions = %+v, want a hammerdb entry — a postgresql dependency must surface registry coverage beyond the drive-tier MCP tools (R-MCP-6)", out.Suggestions)
	}
}

// spec: R-SCOPE-4
func TestDescribeSystem_EverySuggestionCarriesItsTier(t *testing.T) {
	sys := &detect.System{
		SUT:  "checkout-api",
		Deps: []detect.Dep{{Name: "postgres", Type: "postgresql", Address: "postgres:5432"}},
	}

	out := DescribeSystem(sys)

	if len(out.Suggestions) == 0 {
		t.Fatal("Suggestions is empty; this test needs at least one to check for a tier label")
	}
	for _, s := range out.Suggestions {
		if s.Tier == "" {
			t.Errorf("suggestion %q has no Tier — an agent must never be told we execute something we only name (R-SCOPE-4)", s.ID)
		}
	}
}
