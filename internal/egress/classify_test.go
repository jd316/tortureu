package egress_test

import (
	"testing"

	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/egress"
)

// spec: R-DC2-1
func TestClassifyDefaultsUnknownHostToUnclassified(t *testing.T) {
	detected := map[string]string{"api.partner.com": "unclassified"}
	cfg := config.Egress{Default: "deny", Hosts: map[string]config.EgressHost{}}

	classes := egress.Classify(detected, cfg)

	if got := classes["api.partner.com"]; got != egress.ClassUnclassified {
		t.Fatalf("api.partner.com classified %q, want %q (default-deny, R-DC2-1)", got, egress.ClassUnclassified)
	}
}

// spec: R-DET-4
func TestClassifyAppliesUserOverrideOnTopOfDetectedClass(t *testing.T) {
	// detect found this host in the compose graph and labelled it internal,
	// but the user's torture.yaml has explicitly classified it as real
	// (R-DET-4's egress: block is user-editable) — the explicit override
	// wins.
	detected := map[string]string{"api.stripe.com": "unclassified"}
	cfg := config.Egress{
		Default: "deny",
		Hosts: map[string]config.EgressHost{
			"api.stripe.com": {Class: "mock", From: "capture"},
		},
	}

	classes := egress.Classify(detected, cfg)

	if got := classes["api.stripe.com"]; got != egress.ClassMock {
		t.Fatalf("api.stripe.com classified %q, want %q (R-DET-4: user classification in torture.yaml)", got, egress.ClassMock)
	}
}
