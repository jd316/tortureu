package egress_test

import (
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/egress"
)

// spec: R-DC2-2
func TestCheckUnclassifiedNamesEveryOffendingHost(t *testing.T) {
	classes := map[string]egress.Class{
		"postgres:5432":    egress.ClassInternal,
		"api.partner.com":  egress.ClassUnclassified,
		"evil.example.com": egress.ClassUnclassified,
	}

	err := egress.CheckUnclassified(classes)

	if err == nil {
		t.Fatal("CheckUnclassified returned nil, want an error naming the unclassified hosts (R-DC2-2)")
	}
	for _, host := range []string{"api.partner.com", "evil.example.com"} {
		if !strings.Contains(err.Error(), host) {
			t.Errorf("error %q does not name unclassified host %q", err.Error(), host)
		}
	}
	if strings.Contains(err.Error(), "postgres:5432") {
		t.Errorf("error %q wrongly names classified host postgres:5432", err.Error())
	}
}

// spec: R-DC2-2
func TestCheckUnclassifiedPassesWhenAllHostsClassified(t *testing.T) {
	classes := map[string]egress.Class{
		"postgres:5432":  egress.ClassInternal,
		"api.stripe.com": egress.ClassMock,
	}

	if err := egress.CheckUnclassified(classes); err != nil {
		t.Fatalf("CheckUnclassified: %v, want nil (all hosts classified)", err)
	}
}
