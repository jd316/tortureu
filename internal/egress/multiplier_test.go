package egress_test

import (
	"testing"

	"github.com/jd316/TortureU/internal/egress"
)

// spec: R-DC2-4
func TestCheckMultiplierRejectsAboveOneXAgainstRealHostWithoutOptIn(t *testing.T) {
	classes := map[string]egress.Class{"api.partner.com": egress.ClassReal}

	err := egress.CheckMultiplier(classes, 2.0, false)

	if err == nil {
		t.Fatal("CheckMultiplier(2x, allowRealTraffic=false) returned nil, want an error (R-DC2-4)")
	}
}

// spec: R-DC2-4
func TestCheckMultiplierAllowsAboveOneXWithExplicitOptIn(t *testing.T) {
	classes := map[string]egress.Class{"api.partner.com": egress.ClassReal}

	if err := egress.CheckMultiplier(classes, 2.0, true); err != nil {
		t.Fatalf("CheckMultiplier(2x, allowRealTraffic=true): %v, want nil", err)
	}
}

// spec: R-DC2-4
func TestCheckMultiplierAllowsOneXAgainstRealHostWithoutOptIn(t *testing.T) {
	classes := map[string]egress.Class{"api.partner.com": egress.ClassReal}

	if err := egress.CheckMultiplier(classes, 1.0, false); err != nil {
		t.Fatalf("CheckMultiplier(1x, allowRealTraffic=false): %v, want nil (1x needs no opt-in)", err)
	}
}
