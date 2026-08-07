package doctor_test

import (
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/doctor"
)

// spec: R-AUD-1
func TestAuditFlagsMissingTimeoutForDetectedClient(t *testing.T) {
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "db", Type: "postgresql", Clients: []string{"github.com/jackc/pgx"}},
		},
	}

	findings := doctor.Audit(sys)

	var found bool
	for _, f := range findings {
		if f.Check == doctor.CheckTimeout && f.DepType == "postgresql" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a missing-timeout finding for postgresql client, got %+v", findings)
	}
}

// spec: R-AUD-2
func TestAuditFlagsRetryWithoutCapBackoffJitter(t *testing.T) {
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "db", Type: "postgresql", Clients: []string{"github.com/jackc/pgx"}},
		},
	}

	findings := doctor.Audit(sys)

	var found bool
	for _, f := range findings {
		if f.Check == doctor.CheckRetry && f.DepType == "postgresql" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a retry finding for postgresql client, got %+v", findings)
	}
}

// spec: R-AUD-3
func TestAuditFindingsAreHintsNeverFailures(t *testing.T) {
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "db", Type: "postgresql", Clients: []string{"github.com/jackc/pgx"}},
		},
	}

	findings := doctor.Audit(sys)
	if len(findings) == 0 {
		t.Fatal("expected at least one finding to check its level")
	}
	for _, f := range findings {
		if f.Level != doctor.LevelHint {
			t.Fatalf("finding %+v has level %q, want %q — findings must never be failures", f, f.Level, doctor.LevelHint)
		}
	}
}

// spec: R-AUD-4
func TestFindingNamesTheExperimentThatWouldProveIt(t *testing.T) {
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "db", Type: "postgresql", Clients: []string{"github.com/jackc/pgx"}},
		},
	}

	findings := doctor.Audit(sys)
	for _, f := range findings {
		if f.Experiment == "" {
			t.Fatalf("finding %+v does not name an experiment", f)
		}
	}
}

// spec: R-AUD-5
func TestAuditSkipsDependenciesWithoutKnownClientLibrary(t *testing.T) {
	sys := &detect.System{
		Deps: []detect.Dep{
			// image-detected dependency with no matched client library: not a
			// "known library" per R-AUD-1/R-AUD-5, so it must not be audited.
			{Name: "db", Type: "postgresql"},
		},
	}

	findings := doctor.Audit(sys)
	if len(findings) != 0 {
		t.Fatalf("expected no findings for a dependency with no detected client library, got %+v", findings)
	}
}
