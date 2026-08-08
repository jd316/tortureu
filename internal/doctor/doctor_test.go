package doctor_test

import (
	"os"
	"path/filepath"
	"strings"
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

// spec: R-AUD-5
func TestAuditFindsNetHTTPClientMissingTimeout(t *testing.T) {
	// net/http is Go's stdlib HTTP client: it never appears in a go.mod
	// require line, so a dependency reached only through it never gets a
	// detect.Dep.Clients entry (R-DET-5 can't see stdlib) — that is
	// TBD-10's gap. R-AUD-5 permits the audit itself to read source at a
	// known construction site regardless of any manifest signal: finding
	// http.Client{ there is the evidence, independent of Clients.
	dir := writeGoFile(t, `
package main

import "net/http"

func newClient() *http.Client {
	return &http.Client{}
}
`)
	sys := &detect.System{
		SUT: "api",
		Deps: []detect.Dep{
			// No Clients: exactly the case a lockfile-only view of net/http
			// can never populate.
			{Name: "dep", Type: "unclassified"},
		},
	}

	f := findFinding(t, doctor.Audit(dir, sys), doctor.CheckTimeout, "http")
	if f.Library != "net/http" {
		t.Fatalf("Library = %q, want %q", f.Library, "net/http")
	}
	// The finding is a property of the SUT's own code (which host an
	// http.Client calls is not knowable from its construction site
	// alone — R-AUD-5), so it is attributed to sys.SUT, never to an
	// arbitrary dependency.
	if f.DepName != "api" {
		t.Fatalf("DepName = %q, want %q (sys.SUT) — a net/http finding is a property of the SUT, not any one dependency", f.DepName, "api")
	}
	if !f.Determined {
		t.Fatalf("expected timeout to be determined from the http.Client{} construction site, got %+v", f)
	}
	if f.Present {
		t.Fatalf("expected timeout to be reported absent — no Client.Timeout/ResponseHeaderTimeout/TLSHandshakeTimeout set in source, got %+v", f)
	}
}

// spec: R-AUD-4
func TestNetHTTPFindingDoesNotNameAnUnrelatedDependencyAsItsExperiment(t *testing.T) {
	// Regression for a field-verified bug: a compose stack with a Go
	// service using &http.Client{} (no timeout) alongside a postgres
	// dependency previously attached the http finding to "db" (the
	// postgres dependency) and told the user to run a latency fault on
	// postgres to prove an HTTP client's deadline exists — postgres has
	// nothing to do with net/http. Which host an http.Client calls is not
	// knowable from its construction site (R-AUD-5), so naming any
	// dependency as the experiment target would be a guess; a wrong
	// experiment teaches the user something false, which is worse than
	// none (R-AUD-4).
	dir := writeGoFile(t, `
package main

import "net/http"

func newClient() *http.Client {
	return &http.Client{}
}
`)
	sys := &detect.System{
		SUT: "api",
		Deps: []detect.Dep{
			{Name: "db", Type: "postgresql", Clients: []string{"github.com/jackc/pgx/v5"}},
		},
	}

	findings := doctor.Audit(dir, sys)

	var httpFindings int
	for _, f := range findings {
		if f.Library != "net/http" {
			continue
		}
		httpFindings++
		if f.DepName == "db" {
			t.Fatalf("net/http finding attributed to %q (the postgres dependency) — must be attributed to the SUT, not a database it does not talk to: %+v", f.DepName, f)
		}
		if f.DepName != "api" {
			t.Fatalf("DepName = %q, want %q (sys.SUT)", f.DepName, "api")
		}
		if strings.Contains(f.Experiment, "on db") || strings.Contains(f.Experiment, " db ") {
			t.Fatalf("Experiment names the unrelated postgres dependency: %q", f.Experiment)
		}
		if !strings.Contains(f.Experiment, "not determined") {
			t.Fatalf("Experiment should say the target host cannot be determined from source, got %q", f.Experiment)
		}
	}
	if httpFindings == 0 {
		t.Fatal("expected at least one net/http finding")
	}

	// One finding per check (timeout, retry), not one per dependency —
	// there is only one SUT, so re-checking net/http per dependency would
	// just duplicate the same finding.
	if httpFindings != 2 {
		t.Fatalf("got %d net/http findings, want exactly 2 (timeout + retry, reported once each)", httpFindings)
	}
}

// spec: R-AUD-5
func TestAuditConfirmsNetHTTPTimeoutWhenSet(t *testing.T) {
	dir := writeGoFile(t, `
package main

import (
	"net/http"
	"time"
)

func newClient() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}
`)
	sys := &detect.System{
		Deps: []detect.Dep{{Name: "dep", Type: "unclassified"}},
	}

	f := findFinding(t, doctor.Audit(dir, sys), doctor.CheckTimeout, "http")
	if !f.Determined || !f.Present {
		t.Fatalf("expected the Timeout field to be confirmed present, got %+v", f)
	}
}

// spec: R-AUD-6
func TestAuditStaysSilentOnNetHTTPWithNoEvidenceOfUse(t *testing.T) {
	// A dependency with no lockfile client and no http.Client{ anywhere in
	// source gets no http finding at all — R-AUD-6's "not determined" is
	// for a library known to be in use whose setting couldn't be resolved,
	// not for speculatively checking every dependency for a library with
	// no evidence it is used anywhere.
	sys := &detect.System{
		Deps: []detect.Dep{{Name: "dep", Type: "unclassified"}},
	}

	findings := doctor.Audit(t.TempDir(), sys)
	if len(findings) != 0 {
		t.Fatalf("expected no findings when net/http is never constructed anywhere in source, got %+v", findings)
	}
}
