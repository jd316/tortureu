// sqlassert.go evaluates `sql:` assertions (R-CFG-18) — the run-scoped
// data-integrity invariants k6 cannot see and PromQL cannot reach. Until
// TBD-14 was resolved nothing here existed: `internal/run` reported every
// `sql:` entry as unevaluated, so `assert: sql:` could parse and then never
// pass or fail. R-CFG-18 now states the shape, and this file is the thing
// that runs it.
//
// THE SHAPE (R-CFG-18, resolving TBD-14). A `sql:` expression is a
// VIOLATION COUNT: one row, one column, a non-negative number, and the
// invariant holds iff that number is 0. Every other result shape is a tool
// error naming what came back, so the failing-rows reading is refused
// rather than silently taken. SPEC §12's TBD-14 carries the full reasoning;
// the short form is that MySQL has no boolean type, so under the opposite
// (predicate) polarity `select count(*) from orders where total is null`
// returning 3 is truthy and reads as PASS on a violated invariant — a false
// green, which is the one outcome R-VER-8 exists to prevent. This polarity's
// mirrored mistake reads as a violation and FAILS. Only one of the two is
// fail-safe.
//
// HOW IT CONNECTS, AND WHY THERE IS NO DRIVER. This module has no SQL
// driver dependency, and adding one to reach a database that is already
// running in a container next door would be a strange trade. The engine's
// own client (`psql`, `mysql`) is present in every official image, so the
// query runs via `docker exec` inside the database's own container — which
// also sidesteps the DC-2 reachability problem HTTPPromQuerier needs
// fallbackTransport for: there is no host-to-isolated-network hop to make.
//
// WHAT IT REFUSES TO INVENT. A host, a user, a password, a database name.
// All four come from one explicitly supplied URL and none has a default;
// a missing one is refused by name (R-CFG-18). Scanning the wrong database
// and reporting the answer would be worse than not running.
package run

import (
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/detect"
	"github.com/jd316/tortureu/internal/doctor"
	"github.com/jd316/tortureu/internal/verdict"
)

// SQLQuerier evaluates one `sql:` assert expression (R-CFG-18) against the
// database under test. It is the sql-path analogue of PromQuerier: one
// narrow interface, one real implementation (DockerSQLQuerier), fakes in
// tests.
type SQLQuerier interface {
	// Violations runs expr and returns the single non-negative number it
	// computes — the count of rows violating the invariant. Zero means the
	// invariant held. A result that is not one row and one non-negative
	// number is an error, never a count: guessing what a rows-shaped
	// result meant is exactly the ambiguity R-CFG-18 removes. An
	// unreachable database or a query the engine rejects is likewise an
	// error (R-VER-2) — TortureU failing, not the SUT.
	Violations(expr string) (int64, error)
}

// DockerSQLQuerier runs each expression with the database engine's own
// command-line client, inside the container running that database.
type DockerSQLQuerier struct {
	// Engine is an R-DET-9 dependency type: "postgresql" or "mysql".
	Engine string
	// Service is the compose service (or container) name of the database.
	// Resolved to a live container at query time, not at construction:
	// Deps are built before the stack is reset and brought up.
	Service string
	// User, Password and Database are supplied, never derived (R-CFG-18).
	User, Password, Database string
	// Port is optional; empty means the client's own default, which inside
	// the database's own container is always right.
	Port string

	// run executes one command and returns its stdout. A seam so the shape
	// checks can be tested without a database; nil means real exec.
	run func(argv []string, env []string) (string, error)
}

// NewSQLQuerier builds the real querier from one connection URL, e.g.
// postgres://user:pass@db:5432/appdb (scheme postgres|postgresql|mysql).
// Every component is required: nothing here is defaulted or inferred, and
// the error names the missing part (R-CFG-18). The host names the database
// service — the container the client is executed inside.
func NewSQLQuerier(sqlURL string) (SQLQuerier, error) {
	if strings.TrimSpace(sqlURL) == "" {
		return nil, fmt.Errorf("run: sql: no database connection was supplied (--sql-url); " +
			"sql: asserts need a host, user, password and database name, and TortureU does not guess any of them")
	}
	u, err := url.Parse(sqlURL)
	if err != nil {
		return nil, fmt.Errorf("run: sql: connection URL is not a URL: %w", err)
	}
	var engine string
	switch strings.ToLower(u.Scheme) {
	case "postgres", "postgresql":
		engine = "postgresql"
	case "mysql":
		engine = "mysql"
	default:
		return nil, fmt.Errorf("run: sql: unsupported database engine %q in the connection URL: "+
			"sql: asserts are evaluated with the engine's own client, and only postgres:// and mysql:// have one here", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("run: sql: the connection URL names no host; " +
			"it must name the database's compose service or container")
	}
	if u.User == nil || u.User.Username() == "" {
		return nil, fmt.Errorf("run: sql: the connection URL names no user, and TortureU does not guess one")
	}
	password, set := u.User.Password()
	if !set || password == "" {
		return nil, fmt.Errorf("run: sql: the connection URL carries no password for user %q, "+
			"and TortureU does not guess one", u.User.Username())
	}
	database := strings.TrimPrefix(u.Path, "/")
	if database == "" {
		return nil, fmt.Errorf("run: sql: the connection URL names no database, and TortureU does not guess one; " +
			"append /<database> to it")
	}
	return DockerSQLQuerier{
		Engine:   engine,
		Service:  host,
		User:     u.User.Username(),
		Password: password,
		Database: database,
		Port:     u.Port(),
	}, nil
}

// Violations implements SQLQuerier.
func (q DockerSQLQuerier) Violations(expr string) (int64, error) {
	out, err := q.query(expr)
	if err != nil {
		return 0, err
	}
	return parseViolationCount(out, q.Engine)
}

// query executes expr with the engine's own client and returns its stdout.
func (q DockerSQLQuerier) query(expr string) (string, error) {
	run := q.run
	if run == nil {
		container, err := sqlContainerFor(q.Service)
		if err != nil {
			return "", err
		}
		run = func(argv []string, env []string) (string, error) {
			return dockerExec(container, argv, env)
		}
	}
	switch q.Engine {
	case "postgresql":
		// -t: no header or row-count footer. -A: unaligned, so a second
		// column shows up as a "|" this file can detect rather than as
		// padding. -v ON_ERROR_STOP=1: psql otherwise exits 0 after
		// printing the engine's error, which would look like an empty
		// result — a silent failure, not a verdict.
		argv := []string{"psql", "-h", "127.0.0.1"}
		if q.Port != "" {
			argv = append(argv, "-p", q.Port)
		}
		argv = append(argv, "-U", q.User, "-d", q.Database, "-t", "-A", "-v", "ON_ERROR_STOP=1", "-c", expr)
		// PGPASSWORD, not a password on the command line: an argv is
		// visible to every process in the container.
		return run(argv, []string{"PGPASSWORD=" + q.Password})
	case "mysql":
		// -N: no column names. -B: batch mode, tab-separated, so a second
		// column is detectable. MYSQL_PWD for the same reason as
		// PGPASSWORD (mysql's own -p<pass> lands in argv).
		argv := []string{"mysql", "-h", "127.0.0.1"}
		if q.Port != "" {
			argv = append(argv, "-P", q.Port)
		}
		argv = append(argv, "-u", q.User, "-D", q.Database, "-N", "-B", "-e", expr)
		return run(argv, []string{"MYSQL_PWD=" + q.Password})
	default:
		return "", fmt.Errorf("run: sql: unsupported database engine %q", q.Engine)
	}
}

// dockerExec runs argv inside container with env set, returning stdout.
// stderr is folded into the error, because the engine's own complaint about
// a query is the most useful thing this layer can report.
func dockerExec(container string, argv, env []string) (string, error) {
	args := []string{"exec"}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, container)
	args = append(args, argv...)

	cmd := exec.Command("docker", args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("run: sql: query failed in container %s: %w: %s", container, err, msg)
	}
	return stdout.String(), nil
}

// sqlContainerFor resolves a compose service (or container) name to a
// running container. Deliberately its own small function rather than a call
// into run.go's SUT discovery: this one falls back to a plain container
// name, because a database reached by `sql:` need not be a compose service
// of the project under test.
func sqlContainerFor(service string) (string, error) {
	byLabel := exec.Command("docker", "ps", "--filter",
		"label=com.docker.compose.service="+service, "--format", "{{.Names}}")
	if out, err := byLabel.Output(); err == nil {
		if names := strings.Fields(string(out)); len(names) > 0 {
			return names[0], nil
		}
	}
	byName := exec.Command("docker", "ps", "--filter", "name=^"+service+"$", "--format", "{{.Names}}")
	out, err := byName.Output()
	if err != nil {
		return "", fmt.Errorf("run: sql: docker ps: %w", err)
	}
	if names := strings.Fields(string(out)); len(names) > 0 {
		return names[0], nil
	}
	return "", fmt.Errorf("run: sql: no running container found for database %q "+
		"(neither a compose service of that name nor a container of that name); "+
		"sql: asserts run inside the database's own container", service)
}

// parseViolationCount enforces R-CFG-18's one legal result shape on what
// the client printed. Every rejection names the shape that came back, so a
// user who wrote the failing-rows shape is told what to change rather than
// handed a verdict computed from a reading they did not mean.
func parseViolationCount(out, engine string) (int64, error) {
	var rows []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			rows = append(rows, strings.TrimSpace(line))
		}
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("run: sql: the query returned no rows; R-CFG-18 requires exactly one row "+
			"holding the violation count (e.g. select count(*) from (<your query>) t) — %s returned nothing", engine)
	}
	if len(rows) > 1 {
		return 0, fmt.Errorf("run: sql: the query returned more than one row (%d); R-CFG-18 requires exactly one row "+
			"holding the violation count — wrap a row-selecting query as select count(*) from (<your query>) t", len(rows))
	}
	// psql -A separates columns with "|", mysql -B with a tab. A number
	// contains neither, so either one means a second column came back.
	if strings.ContainsAny(rows[0], "|\t") {
		return 0, fmt.Errorf("run: sql: the query returned more than one column (%q); R-CFG-18 requires exactly one, "+
			"holding the violation count — wrap a row-selecting query as select count(*) from (<your query>) t", rows[0])
	}
	n, err := strconv.ParseInt(rows[0], 10, 64)
	if err != nil {
		// A float count is still a count; only a genuinely non-numeric
		// value (NULL, a string, a boolean "t") is refused.
		if f, ferr := strconv.ParseFloat(rows[0], 64); ferr == nil {
			n = int64(f)
		} else {
			return 0, fmt.Errorf("run: sql: the query returned %q, which is not a number; R-CFG-18 requires the "+
				"violation count — a boolean predicate such as count(*) = 0 is not accepted, because MySQL renders it "+
				"as 1/0 and it would be indistinguishable from a count", rows[0])
		}
	}
	if n < 0 {
		return 0, fmt.Errorf("run: sql: the query returned %d, which cannot be a violation count; "+
			"R-CFG-18 requires a non-negative number", n)
	}
	return n, nil
}

// EvaluateSQLAsserts evaluates every `sql:` entry in assert: (R-CFG-18),
// mirroring evaluatePromqlAsserts' lifecycle exactly: a nil querier means no
// connection was configured, and those entries are reported unevaluated
// (R-VER-8, R-COV-6) rather than silently dropped or treated as held
// (R-VER-5); faults/deps feed Cause/Candidates attribution; IDs are assigned
// once by the caller after every finding source is merged.
//
// It differs from the promql path in one deliberate way. A failed PromQL
// query becomes an ambiguous finding; a failed SQL query returns an ERROR.
// R-CFG-18 makes an unreachable database, a rejected query and a result of
// the wrong shape all TortureU failing (R-VER-2 `error`), because each of
// them means the invariant was not evaluated at all — and the alternative,
// a verdict computed from a result nobody could read, is the guess this
// whole resolution exists to remove. A violated invariant, by contrast, is
// a plain result: a finding, with the measured count on it.
func EvaluateSQLAsserts(asserts []config.AssertEntry, q SQLQuerier, faults []config.Fault, sys detect.System, auditFindings []doctor.Finding) ([]verdict.Passed, []verdict.Finding, error) {
	var passed []verdict.Passed
	var findings []verdict.Finding
	for _, entry := range asserts {
		expr, ok := entry["sql"].(string)
		if !ok {
			continue
		}
		assertion := "sql: " + expr
		if q == nil {
			findings = append(findings, unevaluatedFinding(assertion,
				"no database connection configured (--sql-url)"))
			continue
		}
		violations, err := q.Violations(expr)
		if err != nil {
			return nil, nil, fmt.Errorf("evaluating %s: %w", assertion, err)
		}
		observed := fmt.Sprintf("%d violations", violations)
		if violations == 0 {
			passed = append(passed, verdict.Passed{Assertion: assertion, Observed: observed})
			continue
		}
		finding := verdict.Finding{
			Confidence: confidenceFor(len(faults)),
			Broke:      verdict.Broke{Assertion: assertion, Observed: observed},
		}
		attribute(&finding, faults, sys.Deps, auditFindings, sys.Lang, sys.SUT)
		findings = append(findings, finding)
	}
	return passed, findings, nil
}
