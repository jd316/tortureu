package emit

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/jd316/TortureU/internal/detect"
)

// spec: R-CLI-8

const sodaFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://localhost:8080
egress:
  default: deny
  hosts:
    postgres:5432: { class: internal }
load:
  engine: k6
  model: arrival_rate
  stages:
    - phase: peak
      hold: 100rps
      for: 60s
faults:
  - name: pg_slow
    at: peak
    for: 30s
    target: postgres:5432
    inject: { latency: 300ms }
assert:
  - http_req_duration: ["p(95)<500"]
  - sql: 'select count(*) from orders where total is null'
  - sql: 'select count(*) from payments p left join orders o on o.id = p.order_id where o.id is null'
`

func sodaPostgresSystem() *detect.System {
	return &detect.System{
		SUT:  "checkout-api",
		Deps: []detect.Dep{{Name: "postgres", Type: "postgresql", Address: "postgres:5432"}},
	}
}

// spec: R-CLI-8 — "detection never ran" is not "this repo has no database".
func TestSoda_DistinguishesNoDetectionFromNoDatabase(t *testing.T) {
	cfg := mustParse(t, sodaFixture)

	noDetect, err := Soda(cfg, nil)
	if err != nil {
		t.Fatalf("Soda: %v", err)
	}
	if !strings.Contains(noDetect, "could not be detected") {
		t.Errorf("a nil *detect.System must say detection did not run, got:\n%s", noDetect)
	}

	noDB, err := Soda(cfg, &detect.System{SUT: "checkout-api"})
	if err != nil {
		t.Fatalf("Soda: %v", err)
	}
	if noDB == noDetect {
		t.Error("undetected and no-database produced the same output")
	}
	for _, want := range []string{"postgresql", "mysql", "snowflake"} {
		if !strings.Contains(noDB, want) {
			t.Errorf("the refusal must name the dependency types soda covers (%s):\n%s", want, noDB)
		}
	}
	if !NeedsSystem("soda") {
		t.Error("soda must be registered as needing detection: the data source address is a detection fact")
	}
}

// spec: R-CLI-8 — the emitted configuration.yml is the document soda parses:
// a `data_source <name>:` block whose host/port come from detection and whose
// credentials are environment references, never literals.
func TestSoda_ConfigurationComesFromDetectionAndCarriesNoSecrets(t *testing.T) {
	out, err := Soda(mustParse(t, sodaFixture), sodaPostgresSystem())
	if err != nil {
		t.Fatalf("Soda: %v", err)
	}
	doc := sodaExtractYAML(t, out, "SODA_CONFIGURATION")

	block, ok := doc["data_source postgres"].(map[string]any)
	if !ok {
		t.Fatalf("expected a `data_source postgres:` block, got keys %v", sodaKeys(doc))
	}
	if block["type"] != "postgres" {
		t.Errorf("type must be soda's own name for the dependency, got %#v", block["type"])
	}
	if block["host"] != "postgres" {
		t.Errorf("host must come from the detected address, got %#v", block["host"])
	}
	if sodaPortString(block["port"]) != "5432" {
		t.Errorf("port must come from the detected address, got %#v", block["port"])
	}
	for _, field := range []string{"username", "password", "database"} {
		v, _ := block[field].(string)
		if !strings.HasPrefix(v, "${") {
			t.Errorf("%s must be an environment reference, not a literal: %#v", field, block[field])
		}
	}
}

// spec: R-CLI-8 — a scan whose checks file has no active check exits 0 in
// soda 3.x with only a log line ("No valid checks found"). Emitting that
// would be a green result meaning nothing was checked, which is the
// silent-pass this project rejects everywhere.
func TestSoda_RefusesToScanWithNoActiveCheck(t *testing.T) {
	out, err := Soda(mustParse(t, sodaFixture), sodaPostgresSystem())
	if err != nil {
		t.Fatalf("Soda: %v", err)
	}
	if !strings.Contains(out, "No valid checks found") {
		t.Errorf("expected the emitted script to guard against soda's silent zero-check pass:\n%s", out)
	}
}

// spec: R-CFG-18 — a sql: assert is a violation count (one row, one column,
// non-negative, holds iff zero), which is exactly soda's user-defined-metric
// shape. Each assert therefore becomes an ACTIVE check bounding that count at
// zero — the emitter no longer carries them commented out, because TBD-14 is
// resolved and there is no longer a shape to guess.
func TestSoda_EmitsAnActiveViolationCountCheckPerAssert(t *testing.T) {
	out, err := Soda(mustParse(t, sodaFixture), sodaPostgresSystem())
	if err != nil {
		t.Fatalf("Soda: %v", err)
	}
	doc := sodaExtractYAML(t, out, "SODA_CHECKS")
	checks, ok := doc["checks"].([]any)
	if !ok {
		t.Fatalf("expected a top-level `checks:` list, got keys %v", sodaKeys(doc))
	}
	if len(checks) != 2 {
		t.Fatalf("expected one active check per sql: assert, got %d: %#v", len(checks), checks)
	}
	wantSQL := []string{
		"select count(*) from orders where total is null",
		"select count(*) from payments p left join orders o on o.id = p.order_id where o.id is null",
	}
	for i, c := range checks {
		entry, ok := c.(map[string]any)
		if !ok || len(entry) != 1 {
			t.Fatalf("check %d is not a single-key mapping: %#v", i+1, c)
		}
		metric := fmt.Sprintf("tortureu_sql_assert_%d", i+1)
		body, ok := entry[metric+" = 0"].(map[string]any)
		if !ok {
			t.Fatalf("check %d must bound %s at 0 (the invariant holds iff there are no violations), got %v",
				i+1, metric, sodaKeys(entry))
		}
		query, _ := body[metric+" query"].(string)
		if strings.TrimSpace(query) != wantSQL[i] {
			t.Errorf("check %d must carry the assert's SQL verbatim, got %q", i+1, query)
		}
	}
}

// spec: R-CFG-18 — with no sql: assert to evaluate there is nothing to
// check, and the emitted checks file must stay inert so the script's
// no-active-check guard refuses the scan rather than passing on nothing.
func TestSoda_NoSQLAssertsLeavesNoActiveCheck(t *testing.T) {
	cfg := mustParse(t, strings.ReplaceAll(strings.ReplaceAll(sodaFixture,
		"  - sql: 'select count(*) from orders where total is null'\n", ""),
		"  - sql: 'select count(*) from payments p left join orders o on o.id = p.order_id where o.id is null'\n", ""))
	out, err := Soda(cfg, sodaPostgresSystem())
	if err != nil {
		t.Fatalf("Soda: %v", err)
	}
	for _, line := range strings.Split(sodaSection(t, out, "SODA_CHECKS"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		t.Errorf("emitted an active check with no sql: assert to base it on: %q", line)
	}
}

// spec: R-CLI-8 — every fault is reported as untranslated, with the emitter
// that does translate it named.
func TestSoda_ReportsUntranslatedFaults(t *testing.T) {
	out, err := Soda(mustParse(t, sodaFixture), sodaPostgresSystem())
	if err != nil {
		t.Fatalf("Soda: %v", err)
	}
	if !strings.Contains(out, "pg_slow") || !strings.Contains(out, "not translated") {
		t.Errorf("fault pg_slow not reported as untranslated:\n%s", out)
	}
}

// spec: R-CLI-8 — snowflake is a lockfile-only dependency (R-DET-13), so it
// has no address at all. Nothing may be invented for it: account, warehouse
// and database have to come from the environment.
func TestSoda_SnowflakeHasNoAddressToGuess(t *testing.T) {
	sys := &detect.System{
		SUT:  "checkout-api",
		Deps: []detect.Dep{{Name: "snowflake", Type: "snowflake", Clients: []string{"snowflake-connector-python"}}},
	}
	out, err := Soda(mustParse(t, sodaFixture), sys)
	if err != nil {
		t.Fatalf("Soda: %v", err)
	}
	doc := sodaExtractYAML(t, out, "SODA_CONFIGURATION")
	block, ok := doc["data_source snowflake"].(map[string]any)
	if !ok {
		t.Fatalf("expected a `data_source snowflake:` block, got keys %v", sodaKeys(doc))
	}
	if _, bad := block["host"]; bad {
		t.Error("snowflake has no host in soda's connection schema and must not gain one")
	}
	account, _ := block["account"].(string)
	if !strings.HasPrefix(account, "${") {
		t.Errorf("snowflake account must be an environment reference, got %#v", block["account"])
	}
}

func sodaSection(t *testing.T, script, delim string) string {
	t.Helper()
	_, after, ok := strings.Cut(script, "<<'"+delim+"'\n")
	if !ok {
		t.Fatalf("no %s heredoc in the emitted script:\n%s", delim, script)
	}
	body, _, ok := strings.Cut(after, "\n"+delim+"\n")
	if !ok {
		t.Fatalf("unterminated %s heredoc in the emitted script", delim)
	}
	return body
}

func sodaExtractYAML(t *testing.T, script, delim string) map[string]any {
	t.Helper()
	var doc map[string]any
	body := sodaSection(t, script, delim)
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("%s is not valid YAML: %v\n%s", delim, err, body)
	}
	return doc
}

func sodaKeys(doc map[string]any) []string {
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	return keys
}

// sodaPortString renders the port whether soda's YAML carries it as a
// string ('5432', as the soda docs write it) or a bare number.
func sodaPortString(v any) string {
	return fmt.Sprintf("%v", v)
}

// spec: R-CLI-8 — the end-to-end claim in dataquality.go's VERIFICATION
// STATUS block: the configuration.yml this emitter generates is one a real
// soda-core 3.5.6 connects with, and a real check run through it reports the
// right answer both ways round. Off by default (the gate must not need
// Docker or a pip install):
//
//	TORTUREU_EMIT_LIVE=1 go test ./internal/emit/ -run TestSoda_AcceptedByRealSodaCore -v
//
// It does NOT exercise the emitted script's bind mount: this host's Docker
// refuses mounts outside the project directory, so the documents are written
// inside the container instead. That limitation is recorded in the file
// header rather than papered over.
func TestSoda_AcceptedByRealSodaCore(t *testing.T) {
	if os.Getenv("TORTUREU_EMIT_LIVE") != "1" {
		t.Skip("set TORTUREU_EMIT_LIVE=1 (and have Docker) to verify against a real soda-core")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	out, err := Soda(mustParse(t, sodaFixture), sodaPostgresSystem())
	if err != nil {
		t.Fatalf("Soda: %v", err)
	}
	configuration := sodaSection(t, out, "SODA_CONFIGURATION")
	checks := sodaSection(t, out, "SODA_CHECKS")

	const net = "tortureu-soda-verify"
	run := func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		b, err := cmd.CombinedOutput()
		return string(b), err
	}
	_, _ = run("docker", "network", "rm", net)
	if o, err := run("docker", "network", "create", net); err != nil {
		t.Fatalf("docker network create: %v\n%s", err, o)
	}
	defer run("docker", "network", "rm", net)

	_, _ = run("docker", "rm", "-f", "postgres")
	if o, err := run("docker", "run", "-d", "--name", "postgres", "--network", net,
		"-e", "POSTGRES_PASSWORD=tortureu", "postgres:16"); err != nil {
		t.Fatalf("start postgres: %v\n%s", err, o)
	}
	defer run("docker", "rm", "-f", "postgres")

	ready := false
	for i := 0; i < 60; i++ {
		if _, err := run("docker", "exec", "postgres", "pg_isready", "-U", "postgres"); err == nil {
			ready = true
			break
		}
		time.Sleep(time.Second)
	}
	if !ready {
		t.Fatal("postgres never became ready")
	}
	psql := func(sql string) (string, error) {
		return run("docker", "exec", "-e", "PGPASSWORD=tortureu", "postgres",
			"psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-c", sql)
	}
	// Seeded so BOTH emitted checks are violated: one order with no total,
	// and one payment pointing at an order that does not exist.
	if o, err := psql("create table orders (id int, total numeric); " +
		"insert into orders values (1, 10), (2, null); " +
		"create table payments (id int, order_id int); " +
		"insert into payments values (1, 1), (2, 99);"); err != nil {
		t.Fatalf("seed: %v\n%s", err, o)
	}

	scan := func(checks string) (string, int) {
		script := "set -e\n" +
			"pip install --quiet soda-core-postgres==3.5.6\n" +
			"mkdir -p /w && cd /w\n" +
			"cat > configuration.yml <<'CFG'\n" + configuration + "CFG\n" +
			"cat > checks.yml <<'CHK'\n" + checks + "\nCHK\n" +
			"soda scan -d postgres -c configuration.yml checks.yml\n"
		cmd := exec.Command("docker", "run", "--rm", "--network", net,
			"-e", "SODA_POSTGRES_USER=postgres",
			"-e", "SODA_POSTGRES_PASSWORD=tortureu",
			"-e", "SODA_POSTGRES_DATABASE=postgres",
			"-e", "SODA_POSTGRES_SCHEMA=public",
			"python:3.11-slim", "sh", "-c", script)
		b, err := cmd.CombinedOutput()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("docker run: %v\n%s", err, b)
		}
		return string(b), code
	}

	// The emitted checks, unedited, against a database that violates both:
	// soda 3.x must exit 2 (check failures).
	got, code := scan(checks)
	if code != 2 {
		t.Fatalf("expected soda exit 2 (check failures) from the emitted checks, got %d:\n%s", code, got)
	}
	t.Logf("soda failed the emitted checks against violating data:\n%s", got)

	// Repair both invariants and scan the SAME emitted checks again: the
	// verdict must flip, or the checks are not measuring anything.
	if o, err := psql("update orders set total = 0 where total is null; " +
		"delete from payments where order_id not in (select id from orders);"); err != nil {
		t.Fatalf("repair: %v\n%s", err, o)
	}
	got, code = scan(checks)
	if code != 0 {
		t.Fatalf("expected soda exit 0 (all passed) once the invariants hold, got %d:\n%s", code, got)
	}
	t.Logf("soda passed the emitted checks once the data was repaired:\n%s", got)
}
