package egress_test

import (
	"testing"

	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/egress"
)

// spec: R-DC2-6
func TestClassifyFailsClosedOnBogusClassBypassingConfigParse(t *testing.T) {
	// A bogus class string reaching Classify directly, as if config.Parse's
	// own rejection of unknown classes had a gap. Classify must not trust
	// that upstream check ran.
	detected := map[string]string{"api.partner.com": "unclassified"}
	cfg := config.Egress{
		Default: "deny",
		Hosts: map[string]config.EgressHost{
			"api.partner.com": {Class: "totally-bogus"},
		},
	}

	classes := egress.Classify(detected, cfg)

	if got := classes["api.partner.com"]; got != egress.ClassUnclassified {
		t.Fatalf("Classify with bogus class = %q, want %q (R-DC2-6: fail closed)", got, egress.ClassUnclassified)
	}
}

// spec: R-DC2-6
func TestCheckUnclassifiedFailsClosedOnBogusClass(t *testing.T) {
	// Constructed directly, bypassing Classify and config.Parse both — proves
	// CheckUnclassified does not assume its input was already validated.
	classes := map[string]egress.Class{
		"postgres:5432":   egress.ClassInternal,
		"api.partner.com": egress.Class("totally-bogus"),
	}

	err := egress.CheckUnclassified(classes)

	if err == nil {
		t.Fatal("CheckUnclassified with a bogus class returned nil, want an abort (R-DC2-6: fail closed)")
	}
}

// spec: R-DC2-6
func TestAuditFailsClosedOnBogusClass(t *testing.T) {
	classes := map[string]egress.Class{
		"api.partner.com": egress.Class("totally-bogus"),
	}

	got := egress.Audit(classes)

	found := false
	for _, h := range got.Unclassified {
		if h == "api.partner.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Audit(bogus class) = %+v, want api.partner.com in Unclassified — a bogus class must not land in no bucket at all (R-DC2-6)", got)
	}
	total := len(got.Mocked) + len(got.Blocked) + len(got.Real) + len(got.Unclassified)
	if total != 1 {
		t.Fatalf("Audit(bogus class) placed the host in %d buckets total, want exactly 1", total)
	}
}
