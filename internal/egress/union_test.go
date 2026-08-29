package egress_test

import (
	"testing"

	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/egress"
)

// spec: R-DC2-1
func TestClassifyIncludesHostDeclaredInTortureYAMLButAbsentFromDetect(t *testing.T) {
	// detect's EgressClass is populated only for compose services matching
	// its closed image vocabulary (postgres*, redis*, kafka*, ...). A plain
	// custom `dep` service the user nonetheless declared in torture.yaml's
	// egress.hosts is the strongest possible signal of intent and MUST NOT
	// be silently dropped just because detect never recognised it.
	detected := map[string]string{} // detect found nothing at all
	cfg := config.Egress{
		Default: "deny",
		Hosts: map[string]config.EgressHost{
			"custom-dep:9000": {Class: "internal"},
		},
	}

	classes := egress.Classify(detected, cfg)

	got, ok := classes["custom-dep:9000"]
	if !ok {
		t.Fatal("custom-dep:9000 is missing from Classify's output entirely — a host declared in torture.yaml vanished instead of being classified")
	}
	if got != egress.ClassInternal {
		t.Fatalf("custom-dep:9000 classified %q, want %q", got, egress.ClassInternal)
	}
}

// spec: R-DC2-2
func TestClassifyNeverSilentlyDropsADeclaredHostEvenWhenUnclassifiable(t *testing.T) {
	// A host present in neither detect's output nor given a recognised
	// class in torture.yaml must still surface as unclassified so
	// CheckUnclassified can abort on it — never vanish (R-DET-3/R-DET-7:
	// an unknown must surface, never disappear).
	detected := map[string]string{}
	cfg := config.Egress{
		Default: "deny",
		Hosts: map[string]config.EgressHost{
			"mystery-dep:1234": {}, // no class set at all
		},
	}

	classes := egress.Classify(detected, cfg)

	got, ok := classes["mystery-dep:1234"]
	if !ok {
		t.Fatal("mystery-dep:1234 is missing from Classify's output entirely — it must reach unclassified, not vanish")
	}
	if got != egress.ClassUnclassified {
		t.Fatalf("mystery-dep:1234 classified %q, want %q", got, egress.ClassUnclassified)
	}

	if err := egress.CheckUnclassified(classes); err == nil {
		t.Fatal("CheckUnclassified returned nil for a run with a previously-vanishing host, want an abort")
	}
}
