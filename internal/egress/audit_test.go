package egress_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/jd316/TortureU/internal/egress"
	"github.com/jd316/TortureU/internal/verdict"
)

// spec: R-VER-6
func TestAuditSortsHostsIntoVerdictEgressAuditBuckets(t *testing.T) {
	classes := map[string]egress.Class{
		"postgres:5432":     egress.ClassInternal,
		"api.stripe.com":    egress.ClassMock,
		"telemetry.vendor":  egress.ClassBlock,
		"api.partner.com":   egress.ClassReal,
		"unknown.evil.host": egress.ClassUnclassified,
	}

	got := egress.Audit(classes)
	sort.Strings(got.Mocked)
	sort.Strings(got.Blocked)
	sort.Strings(got.Real)
	sort.Strings(got.Unclassified)

	want := verdict.EgressAudit{
		Mocked:       []string{"api.stripe.com"},
		Blocked:      []string{"telemetry.vendor"},
		Real:         []string{"api.partner.com"},
		Unclassified: []string{"unknown.evil.host"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Audit() = %+v, want %+v (R-VER-6; note: internal hosts are excluded)", got, want)
	}
}
