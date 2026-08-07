package doctor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jdb316/tortureu/internal/detect"
	"github.com/jdb316/tortureu/internal/doctor"
)

// writeGoFile writes a Go source file into a fresh temp dir and returns the
// dir, so Audit's bounded source inspection (R-AUD-5) has a real
// construction site to read.
func writeGoFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func findFinding(t *testing.T, findings []doctor.Finding, check doctor.Check, depType string) doctor.Finding {
	t.Helper()
	for _, f := range findings {
		if f.Check == check && f.DepType == depType {
			return f
		}
	}
	t.Fatalf("no %s finding for dep type %q in %+v", check, depType, findings)
	return doctor.Finding{}
}

// spec: R-AUD-1
func TestAuditFlagsMissingTimeoutForDetectedClient(t *testing.T) {
	dir := writeGoFile(t, `
package main

import "github.com/jackc/pgx/v5/pgxpool"

func connect() {
	pgxpool.New(nil, "postgres://localhost")
}
`)
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "db", Type: "postgresql", Clients: []string{"github.com/jackc/pgx/v5"}},
		},
	}

	f := findFinding(t, doctor.Audit(dir, sys), doctor.CheckTimeout, "postgresql")
	if !f.Determined {
		t.Fatalf("expected timeout to be determined from the constructor call, got %+v", f)
	}
	if f.Present {
		t.Fatalf("expected timeout to be reported absent (no ConnectTimeout/context deadline in source), got %+v", f)
	}
}

// spec: R-AUD-1
// spec: R-AUD-6
func TestAuditConfirmsTimeoutWhenSourceConfiguresOne(t *testing.T) {
	dir := writeGoFile(t, `
package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func connect() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pgxpool.New(ctx, "postgres://localhost")
}
`)
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "db", Type: "postgresql", Clients: []string{"github.com/jackc/pgx/v5"}},
		},
	}

	f := findFinding(t, doctor.Audit(dir, sys), doctor.CheckTimeout, "postgresql")
	if !f.Determined {
		t.Fatalf("expected timeout to be determined from the constructor call, got %+v", f)
	}
	if !f.Present {
		t.Fatalf("expected timeout to be confirmed present (context.WithTimeout in source), got %+v", f)
	}
}

// spec: R-AUD-2
func TestAuditFlagsRetryWithoutCapBackoffJitter(t *testing.T) {
	dir := writeGoFile(t, `
package main

import "github.com/jackc/pgx/v5/pgxpool"

// retries the connect call, but with no cap, backoff, or jitter.
func connectWithRetry() {
	for {
		if _, err := pgxpool.New(nil, "postgres://localhost"); err == nil {
			return
		}
	}
}
`)
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "db", Type: "postgresql", Clients: []string{"github.com/jackc/pgx/v5"}},
		},
	}

	f := findFinding(t, doctor.Audit(dir, sys), doctor.CheckRetry, "postgresql")
	if !f.Determined {
		t.Fatalf("expected retry config to be determined from the constructor's file, got %+v", f)
	}
	if f.Present {
		t.Fatalf("expected retry to be reported lacking a cap/backoff/jitter, got %+v", f)
	}
}

// spec: R-AUD-3
func TestAuditFindingsAreHintsNeverFailures(t *testing.T) {
	dir := writeGoFile(t, `
package main

import "github.com/jackc/pgx/v5/pgxpool"

func connect() { pgxpool.New(nil, "postgres://localhost") }
`)
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "db", Type: "postgresql", Clients: []string{"github.com/jackc/pgx/v5"}},
		},
	}

	findings := doctor.Audit(dir, sys)
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
			// no source available in this temp dir: exercises the
			// not-determined path too, which must still name an experiment.
			{Name: "db", Type: "postgresql", Clients: []string{"github.com/jackc/pgx/v5"}},
		},
	}

	findings := doctor.Audit(t.TempDir(), sys)
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

	findings := doctor.Audit(t.TempDir(), sys)
	if len(findings) != 0 {
		t.Fatalf("expected no findings for a dependency with no detected client library, got %+v", findings)
	}
}

// spec: R-AUD-6
func TestAuditReportsNotDeterminedForLibraryOutsideItsTable(t *testing.T) {
	sys := &detect.System{
		Deps: []detect.Dep{
			// kafka has no entry in doctor's bounded construction-site
			// table: the audit must say "not determined", never assert
			// there is no timeout or retry cap.
			{Name: "broker", Type: "kafka", Clients: []string{"github.com/IBM/sarama"}},
		},
	}

	findings := doctor.Audit(t.TempDir(), sys)
	f := findFinding(t, findings, doctor.CheckTimeout, "kafka")
	if f.Determined {
		t.Fatalf("expected timeout to be not-determined for a library outside doctor's table, got %+v", f)
	}
	if f.Present {
		t.Fatalf("an undetermined finding must never assert presence, got %+v", f)
	}
}

// spec: R-AUD-6
func TestAuditReportsNotDeterminedWhenConstructionSiteNotFound(t *testing.T) {
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "db", Type: "postgresql", Clients: []string{"github.com/jackc/pgx/v5"}},
		},
	}

	// empty dir: postgresql is a known library, but there is no source to
	// find its construction call in, so the audit must not guess absence.
	findings := doctor.Audit(t.TempDir(), sys)
	f := findFinding(t, findings, doctor.CheckTimeout, "postgresql")
	if f.Determined {
		t.Fatalf("expected not-determined when no construction site is found, got %+v", f)
	}
	if f.Present {
		t.Fatalf("an undetermined finding must never assert presence, got %+v", f)
	}
}

// spec: R-AUD-6
func TestAuditNeverAttributesASharedConstructorToTheWrongDriver(t *testing.T) {
	// sql.Open is database/sql's driver-generic entry point. A file that
	// only imports the mysql driver and calls sql.Open must never be read
	// as postgres's construction site — that would ground a confident
	// finding in the wrong dependency's code, which is worse than an
	// honest "not determined" (review finding, fix round 1).
	dir := writeGoFile(t, `
package main

import (
	"database/sql"

	_ "github.com/go-sql-driver/mysql"
)

func connect() {
	sql.Open("mysql", "user:pass@/dbname")
}
`)
	sys := &detect.System{
		Deps: []detect.Dep{
			{Name: "pg", Type: "postgresql", Clients: []string{"github.com/lib/pq"}},
			{Name: "db", Type: "mysql", Clients: []string{"github.com/go-sql-driver/mysql"}},
		},
	}

	findings := doctor.Audit(dir, sys)

	pg := findFinding(t, findings, doctor.CheckTimeout, "postgresql")
	if pg.Determined {
		t.Fatalf("postgresql has no driver import in this file — must be not-determined, got %+v", pg)
	}
	if pg.Present {
		t.Fatalf("an undetermined finding must never assert presence, got %+v", pg)
	}

	my := findFinding(t, findings, doctor.CheckTimeout, "mysql")
	if !my.Determined {
		t.Fatalf("mysql has driver corroboration in this file — should be determined, got %+v", my)
	}
}
