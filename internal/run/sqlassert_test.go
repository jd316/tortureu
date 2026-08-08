// sqlassert_test.go proves the sql: assert path (R-CFG-18) in both
// directions and at every refusal boundary. The fake-querier tests pin the
// verdict semantics; the two tests at the bottom run the real thing against
// a real postgres and a real mysql container, because a data-integrity
// invariant that has only ever been evaluated against a fake is not
// evidence that it can be evaluated at all.
package run

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
)

// fakeSQLQuerier answers each expression from a table, so a test can pin
// the verdict a given violation count produces without a database.
type fakeSQLQuerier struct {
	counts map[string]int64
	err    error
	asked  []string
}

func (f *fakeSQLQuerier) Violations(expr string) (int64, error) {
	f.asked = append(f.asked, expr)
	if f.err != nil {
		return 0, f.err
	}
	return f.counts[expr], nil
}

// spec: R-CFG-18
//
// Zero violations is the invariant holding: a Passed entry carrying the
// measured count, and no finding.
func TestEvaluateSQLAsserts_ZeroViolationsHolds(t *testing.T) {
	expr := "select count(*) from orders where total is null"
	asserts := []config.AssertEntry{{"sql": expr}}
	q := &fakeSQLQuerier{counts: map[string]int64{expr: 0}}

	passed, findings, err := EvaluateSQLAsserts(asserts, q, nil, detect.System{}, nil)
	if err != nil {
		t.Fatalf("a held invariant is not a tool error: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
	if len(passed) != 1 {
		t.Fatalf("expected 1 passed assertion, got %#v", passed)
	}
	if passed[0].Assertion != "sql: "+expr {
		t.Errorf("assertion text: %q", passed[0].Assertion)
	}
	if passed[0].Observed != "0 violations" {
		t.Errorf("observed should be the measured count, got %q", passed[0].Observed)
	}
}

// spec: R-VER-2
//
// A violated invariant is a RESULT, not a tool error: a finding, with the
// count it actually measured as the observed value.
func TestEvaluateSQLAsserts_ViolationsAreAFinding(t *testing.T) {
	expr := "select count(*) from orders where total is null"
	asserts := []config.AssertEntry{{"sql": expr}}
	q := &fakeSQLQuerier{counts: map[string]int64{expr: 3}}

	passed, findings, err := EvaluateSQLAsserts(asserts, q, nil, detect.System{}, nil)
	if err != nil {
		t.Fatalf("a violated invariant must not be reported as a tool error: %v", err)
	}
	if len(passed) != 0 {
		t.Fatalf("a violated invariant must not appear as passed: %#v", passed)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %#v", findings)
	}
	if findings[0].Unevaluated {
		t.Error("a query that ran is not unevaluated")
	}
	if findings[0].Broke.Observed != "3 violations" {
		t.Errorf("observed should carry the measured count, got %q", findings[0].Broke.Observed)
	}
}

// spec: R-VER-2
//
// A database that cannot be reached, or a query the engine rejects, is
// TortureU failing — an error returned to the caller, never a passing
// invariant and never a silent skip.
func TestEvaluateSQLAsserts_QueryErrorIsAToolError(t *testing.T) {
	asserts := []config.AssertEntry{{"sql": "select count(*) from orders"}}
	q := &fakeSQLQuerier{err: errors.New("connection refused")}

	passed, _, err := EvaluateSQLAsserts(asserts, q, nil, detect.System{}, nil)
	if err == nil {
		t.Fatal("an unreachable database must be a tool error")
	}
	if len(passed) != 0 {
		t.Fatalf("nothing may be reported as passing when the query failed: %#v", passed)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("the error must carry the cause, got %q", err)
	}
}

// spec: R-VER-8
//
// With no connection configured, a sql: assert is unevaluated — never a
// held assertion, never a fabricated failure.
func TestEvaluateSQLAsserts_NoConnectionIsUnevaluated(t *testing.T) {
	asserts := []config.AssertEntry{{"sql": "select count(*) from orders"}}

	passed, findings, err := EvaluateSQLAsserts(asserts, nil, nil, detect.System{}, nil)
	if err != nil {
		t.Fatalf("an unconfigured connection is a reportable state, not a tool error: %v", err)
	}
	if len(passed) != 0 {
		t.Fatalf("an unevaluated assert must not read as held: %#v", passed)
	}
	if len(findings) != 1 || !findings[0].Unevaluated {
		t.Fatalf("expected one unevaluated finding, got %#v", findings)
	}
	if !strings.Contains(findings[0].Reason, "--sql-url") {
		t.Errorf("the reason must name what is missing, got %q", findings[0].Reason)
	}
}

// spec: R-CFG-18
//
// The connection is never guessed: every missing part is refused by name,
// and a URL that is complete is accepted.
func TestNewSQLQuerier_RefusesToGuessAConnection(t *testing.T) {
	refused := []struct{ name, url, want string }{
		{"no url at all", "", "no database connection"},
		{"unknown engine", "sqlite:///tmp/x.db", "engine"},
		{"no user", "postgres://db:5432/appdb", "user"},
		{"no password", "postgres://app@db:5432/appdb", "password"},
		{"no host", "postgres://app:pw@/appdb", "host"},
		{"no database name", "mysql://app:pw@db:3306/", "database"},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			q, err := NewSQLQuerier(tc.url)
			if err == nil {
				t.Fatalf("expected a refusal, got a querier: %#v", q)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal must name what is missing (%q), got %q", tc.want, err)
			}
		})
	}

	q, err := NewSQLQuerier("postgres://app:pw@db:5432/appdb")
	if err != nil {
		t.Fatalf("a complete connection must be accepted: %v", err)
	}
	if q == nil {
		t.Fatal("a complete connection must produce a querier")
	}
}

// spec: R-CFG-18
//
// The one legal result shape is one row, one column, a non-negative
// number. Every other shape is an error naming what came back, so a
// rows-shaped query can never be silently reinterpreted as a count.
func TestDockerSQLQuerier_RefusesEveryShapeButAViolationCount(t *testing.T) {
	cases := []struct{ name, out, want string }{
		{"multiple rows", "1\n2\n3\n", "more than one row"},
		{"multiple columns", "42|7\n", "more than one column"},
		{"no rows", "\n", "no rows"},
		{"null", "NULL\n", "not a number"},
		{"non-numeric", "banana\n", "not a number"},
		{"negative", "-1\n", "negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := DockerSQLQuerier{
				Engine: "postgresql", Service: "db", User: "u", Password: "p", Database: "d",
				run: func([]string, []string) (string, error) { return tc.out, nil },
			}
			n, err := q.Violations("select whatever")
			if err == nil {
				t.Fatalf("expected a shape error, got %d", n)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error must name the shape (%q), got %q", tc.want, err)
			}
		})
	}

	q := DockerSQLQuerier{
		Engine: "postgresql", Service: "db", User: "u", Password: "p", Database: "d",
		run: func([]string, []string) (string, error) { return "4\n", nil },
	}
	n, err := q.Violations("select count(*) from orders where total is null")
	if err != nil {
		t.Fatalf("a single non-negative number is the legal shape: %v", err)
	}
	if n != 4 {
		t.Errorf("violations = %d, want 4", n)
	}
}

// ---- the real thing --------------------------------------------------

// startDB brings up a real database container of the given engine, seeded
// with an orders table holding one row that violates the invariant
// "every order has a total" and two that do not. It returns the container
// name, which doubles as the URL host: the querier resolves a host to the
// container running it.
func startDB(t *testing.T, engine string) string {
	t.Helper()
	requireBinary(t, "docker")
	port := freePort(t)
	name := fmt.Sprintf("tortureu-sqlassert-%s-%d", engine, port)

	var args []string
	switch engine {
	case "postgresql":
		args = []string{"run", "-d", "--rm", "--name", name,
			"-e", "POSTGRES_PASSWORD=torturepw", "-e", "POSTGRES_USER=tortureu",
			"-e", "POSTGRES_DB=torturedb", "postgres:16-alpine"}
	case "mysql":
		args = []string{"run", "-d", "--rm", "--name", name,
			"-e", "MYSQL_ROOT_PASSWORD=torturepw", "-e", "MYSQL_DATABASE=torturedb",
			"-e", "MYSQL_USER=tortureu", "-e", "MYSQL_PASSWORD=torturepw", "mysql:8.4"}
	}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		t.Skipf("could not start a %s container (%v): %s", engine, err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	// Both images start a temporary server during initialisation and then
	// restart it, so one successful probe is not readiness (the same trap
	// startPostgres documents in drive_real_test.go): wait for two in a row.
	probe := func() error {
		switch engine {
		case "postgresql":
			return exec.Command("docker", "exec", "-e", "PGPASSWORD=torturepw", name,
				"psql", "-h", "127.0.0.1", "-U", "tortureu", "-d", "torturedb", "-c", "select 1").Run()
		default:
			return exec.Command("docker", "exec", "-e", "MYSQL_PWD=torturepw", name,
				"mysql", "-h", "127.0.0.1", "-u", "tortureu", "-D", "torturedb", "-e", "select 1").Run()
		}
	}
	deadline := time.Now().Add(180 * time.Second)
	streak := 0
	for time.Now().Before(deadline) {
		if probe() == nil {
			streak++
			if streak >= 2 {
				seedOrders(t, engine, name)
				return name
			}
		} else {
			streak = 0
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Skipf("%s container never became ready", engine)
	return ""
}

// seedOrders creates the table this test asserts over: three orders, one of
// which violates "every order has a total".
func seedOrders(t *testing.T, engine, container string) {
	t.Helper()
	ddl := "create table orders (id int, total int); " +
		"insert into orders values (1, 100); " +
		"insert into orders values (2, 200); " +
		"insert into orders values (3, null);"
	var cmd *exec.Cmd
	if engine == "postgresql" {
		cmd = exec.Command("docker", "exec", "-e", "PGPASSWORD=torturepw", container,
			"psql", "-h", "127.0.0.1", "-U", "tortureu", "-d", "torturedb", "-c", ddl)
	} else {
		cmd = exec.Command("docker", "exec", "-e", "MYSQL_PWD=torturepw", container,
			"mysql", "-h", "127.0.0.1", "-u", "tortureu", "-D", "torturedb", "-e", ddl)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed %s: %v: %s", engine, err, out)
	}
}

// deleteViolatingRow makes the invariant hold, so the same assertion can be
// proven to flip from fail to pass against the same real database.
func deleteViolatingRow(t *testing.T, engine, container string) {
	t.Helper()
	sql := "delete from orders where total is null;"
	var cmd *exec.Cmd
	if engine == "postgresql" {
		cmd = exec.Command("docker", "exec", "-e", "PGPASSWORD=torturepw", container,
			"psql", "-h", "127.0.0.1", "-U", "tortureu", "-d", "torturedb", "-c", sql)
	} else {
		cmd = exec.Command("docker", "exec", "-e", "MYSQL_PWD=torturepw", container,
			"mysql", "-h", "127.0.0.1", "-u", "tortureu", "-D", "torturedb", "-e", sql)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fix the violation in %s: %v: %s", engine, err, out)
	}
}

// spec: R-CFG-18
//
// The whole path against a real database of each supported engine: a
// violated invariant fails with the real count, the same assertion passes
// once the violating row is gone, and a rows-shaped query is refused rather
// than reinterpreted.
func TestDockerSQLQuerier_AgainstRealDatabases(t *testing.T) {
	for _, engine := range []string{"postgresql", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			dockerAvailable(t)
			container := startDB(t, engine)
			scheme := "postgres"
			if engine == "mysql" {
				scheme = "mysql"
			}
			q, err := NewSQLQuerier(fmt.Sprintf("%s://tortureu:torturepw@%s/torturedb", scheme, container))
			if err != nil {
				t.Fatalf("NewSQLQuerier: %v", err)
			}

			expr := "select count(*) from orders where total is null"
			asserts := []config.AssertEntry{{"sql": expr}}

			passed, findings, err := EvaluateSQLAsserts(asserts, q, nil, detect.System{}, nil)
			if err != nil {
				t.Fatalf("a reachable database and a valid query is not a tool error: %v", err)
			}
			if len(passed) != 0 || len(findings) != 1 {
				t.Fatalf("a violated invariant must fail: passed=%#v findings=%#v", passed, findings)
			}
			if findings[0].Broke.Observed != "1 violations" {
				t.Errorf("observed = %q, want the real count from the database", findings[0].Broke.Observed)
			}

			deleteViolatingRow(t, engine, container)

			passed, findings, err = EvaluateSQLAsserts(asserts, q, nil, detect.System{}, nil)
			if err != nil {
				t.Fatalf("second evaluation: %v", err)
			}
			if len(findings) != 0 || len(passed) != 1 {
				t.Fatalf("a held invariant must pass: passed=%#v findings=%#v", passed, findings)
			}
			if passed[0].Observed != "0 violations" {
				t.Errorf("observed = %q, want 0 violations", passed[0].Observed)
			}

			// A rows-shaped query against the real engine: refused by
			// shape, so R-CFG-18's "cannot be silently reinterpreted"
			// holds against real result sets, not just parsed strings.
			seedOrders := "select id, total from orders"
			if _, err := q.Violations(seedOrders); err == nil {
				t.Error("a multi-column result must be refused, not counted")
			}
		})
	}
}
