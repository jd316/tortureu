// drive_real_test.go exercises the two drive-tier runners against the real
// binaries and a real database, not fakes — the fakes in drive_test.go
// prove the orchestration, and these prove the adapters actually work.
//
// Measured on this machine while writing them (pgbench 18.4 against a real
// postgres:16-alpine container, schemathesis 4.24.3 against a real HTTP
// service): pgbench sustained ~450-480 tps at 8 clients / 2 jobs, and
// schemathesis found the planted 500 in under a second. The numbers are in
// the task report; the assertions here are deliberately shape-based (tps >
// 0, the failing operation named) rather than pinned to a throughput this
// machine happened to produce.
//
// Each test skips when its prerequisite is absent rather than failing:
// pgbench, docker and schemathesis are all optional on a developer machine,
// and a skipped test says so, where a failing one would just be noise.
package run

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireBinary(t *testing.T, names ...string) string {
	t.Helper()
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	t.Skipf("none of %v is on PATH", names)
	return ""
}

// freePort asks the kernel for an unused TCP port, so a real-infrastructure
// test never collides with whatever else is running on this machine.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startPostgres brings up a real postgres container and returns its DSN.
func startPostgres(t *testing.T) string {
	t.Helper()
	requireBinary(t, "docker")
	port := freePort(t)
	name := fmt.Sprintf("tortureu-dbload-test-%d", port)
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-e", "POSTGRES_PASSWORD=torturepw",
		"-e", "POSTGRES_USER=tortureu",
		"-e", "POSTGRES_DB=torturedb",
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", port),
		"postgres:16-alpine").CombinedOutput()
	if err != nil {
		t.Skipf("could not start a postgres container (%v): %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	dsn := fmt.Sprintf("postgresql://tortureu:torturepw@127.0.0.1:%d/torturedb", port)
	// pg_isready alone is not enough: the official image starts a temporary
	// server for initdb and then restarts it, so a probe can succeed and
	// the very next connection be refused ("the server terminated
	// abnormally", observed here on the first run of this test). Waiting
	// for a real query to succeed twice in a row, against the real
	// database, is what actually means ready.
	deadline := time.Now().Add(90 * time.Second)
	streak := 0
	for time.Now().Before(deadline) {
		if err := exec.Command("docker", "exec", name, "psql", "-U", "tortureu", "-d", "torturedb", "-c", "select 1").Run(); err == nil {
			streak++
			if streak >= 2 {
				return dsn
			}
		} else {
			streak = 0
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Skip("postgres container never became ready")
	return ""
}

// spec: R-EXE-26
//
// The real thing: pgbench initializing and saturating a live PostgreSQL,
// then being cut short the way the end of a load run cuts it short, with
// the achieved throughput still recovered from its own output.
func TestPgbenchRunner_DrivesRealPostgresAndReportsThroughput(t *testing.T) {
	requireBinary(t, "pgbench")
	dsn := startPostgres(t)

	// 1. A run that ends by itself: the full summary is available.
	h, err := PgbenchRunner{}.Start(dsn, 3*time.Second)
	if err != nil {
		t.Fatalf("Start against a real database: %v", err)
	}
	time.Sleep(5 * time.Second)
	res := h.Stop()
	if res.Err != nil {
		t.Fatalf("pgbench reported an error against a healthy database: %v", res.Err)
	}
	if res.CutShort {
		t.Error("a pgbench that finished its own -T window was reported as cut short")
	}
	if res.TPS <= 0 || res.Transactions <= 0 {
		t.Fatalf("no throughput recovered from pgbench's output: %+v", res)
	}
	if res.Clients != 8 {
		t.Errorf("Clients = %d, want the 8 the runner drove", res.Clients)
	}
	t.Logf("pgbench (natural end): %.1f tps over %.1fs, %d transactions, %d clients",
		res.TPS, res.DurationS, res.Transactions, res.Clients)

	// 2. A run the orchestrator cuts short, which is the normal path: the
	// load ends first and pgbench is stopped mid-flight. pgbench prints no
	// final summary on a signal (measured), so the last -P progress line is
	// what carries the number — and the result must say it is partial.
	h2, err := PgbenchRunner{}.Start(dsn, 5*time.Minute)
	if err != nil {
		t.Fatalf("Start (cut-short case): %v", err)
	}
	time.Sleep(7 * time.Second)
	res2 := h2.Stop()
	if res2.Err != nil {
		t.Fatalf("stopping pgbench mid-run was reported as a tool failure: %v", res2.Err)
	}
	if !res2.CutShort {
		t.Error("a pgbench stopped by the run was not marked cut short")
	}
	if res2.TPS <= 0 {
		t.Fatalf("no throughput recovered from a cut-short pgbench: %+v", res2)
	}
	t.Logf("pgbench (cut short by the run): %.1f tps over %.1fs", res2.TPS, res2.DurationS)

	// Stop is idempotent: teardownAll can reach it more than once.
	if again := h2.Stop(); again.TPS != res2.TPS {
		t.Errorf("Stop is not idempotent: %+v vs %+v", again, res2)
	}
}

// spec: R-EXE-26
//
// A database that cannot be reached is TortureU failing (R-VER-2's error),
// and it must say why in pgbench's own words rather than a shrug.
func TestPgbenchRunner_UnreachableDatabaseIsAToolError(t *testing.T) {
	requireBinary(t, "pgbench")
	port := freePort(t)
	_, err := PgbenchRunner{}.Start(fmt.Sprintf("postgresql://nobody:nothing@127.0.0.1:%d/nodb", port), 5*time.Second)
	if err == nil {
		t.Fatal("Start against a closed port returned no error")
	}
	if !strings.Contains(err.Error(), "connection") && !strings.Contains(err.Error(), "connect") {
		t.Errorf("error %q does not quote pgbench's own reason", err)
	}
}

// fuzzTestSpec is a minimal, real OpenAPI document: one operation that
// works and one that returns 500.
const fuzzTestSpec = `openapi: 3.0.0
info: { title: tortureu-fuzz-fixture, version: "1.0" }
paths:
  /ok:
    get:
      responses:
        "200": { description: ok }
  /boom:
    get:
      responses:
        "200": { description: ok }
`

// spec: R-EXE-27
//
// The real thing: schemathesis against a live service whose /boom returns
// 500 for every request. The 500 is a *result* — it must arrive as
// failures, with Err nil, because reporting it as a tool error would send
// the user to debug TortureU instead of their API (R-VER-2).
func TestSchemathesisRunner_FindsARealServerError(t *testing.T) {
	requireBinary(t, "st", "schemathesis")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/boom") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(spec, []byte(fuzzTestSpec), 0o644); err != nil {
		t.Fatal(err)
	}

	h, err := SchemathesisRunner{MaxExamples: 5, Dir: dir}.Start(spec, srv.URL, 2*time.Minute)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Give the fuzzer time to finish on its own; this fixture takes well
	// under a second in practice.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(filepath.Join(dir, "..")); statErr != nil {
			break
		}
		if procExited(h) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	res := h.Stop()

	if res.Err != nil {
		t.Fatalf("a fuzzer finding a 500 was reported as a tool error (R-VER-2): %v", res.Err)
	}
	if len(res.Failures) == 0 {
		t.Fatal("schemathesis found no failure against a service that 500s")
	}
	var named bool
	for _, f := range res.Failures {
		t.Logf("fuzz failure: %s -> %s", f.Operation, f.Detail)
		if strings.Contains(f.Operation, "/boom") {
			named = true
		}
	}
	if !named {
		t.Errorf("no failure named the operation that actually breaks: %+v", res.Failures)
	}
}

// procExited reports whether a schemathesis handle's process has finished,
// without consuming its result.
func procExited(h FuzzHandle) bool {
	sh, ok := h.(*schemathesisHandle)
	if !ok {
		return true
	}
	select {
	case <-sh.exited:
		return true
	default:
		return false
	}
}

// spec: R-EXE-27
//
// A target no case could reach produces no result at all, and reporting
// that as a clean fuzz pass would be the silent omission this project
// rejects. Measured: schemathesis still exits 1, so the JUnit report's
// error/failure split — not the exit status — is what decides.
func TestSchemathesisRunner_UnreachableTargetIsAToolError(t *testing.T) {
	requireBinary(t, "st", "schemathesis")
	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(spec, []byte(fuzzTestSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", freePort(t))

	h, err := SchemathesisRunner{MaxExamples: 2, Dir: dir}.Start(spec, url, 2*time.Minute)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && !procExited(h) {
		time.Sleep(100 * time.Millisecond)
	}
	res := h.Stop()

	if len(res.Failures) > 0 {
		t.Errorf("an unreachable target produced findings: %+v", res.Failures)
	}
	if res.Err == nil {
		t.Fatal("an unreachable target was reported as a clean fuzz pass")
	}
	t.Logf("unreachable target reported as: %v", res.Err)
}

// --- TBD-12: reaching a target R-DC2-3 isolated on an internal:true network ---
//
// Everything above drives a target with a published port. These two drive
// one with none at all, which is the topology TortureU itself creates
// (R-DC2-3) and the one both flags previously could not work in.

// internalNetwork creates a real `internal: true` Docker network — the same
// kind ComposeTopologyApplier's overlay creates for the SUT — and returns
// its name.
func internalNetwork(t *testing.T, name string) string {
	t.Helper()
	requireBinary(t, "docker")
	if out, err := exec.Command("docker", "network", "create", "--internal", name).CombinedOutput(); err != nil {
		t.Skipf("could not create an internal network (%v): %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", name).Run() })
	return name
}

// requireNoPublishedPort fails the test if the container has any published
// port: without that, the test would silently stop exercising the
// internal-only case it exists for.
func requireNoPublishedPort(t *testing.T, container string) {
	t.Helper()
	out, err := exec.Command("docker", "port", container).CombinedOutput()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		t.Fatalf("%s has a published port (%s) — this test no longer exercises the internal-only-network case", container, out)
	}
}

// spec: R-EXE-26
//
// The real thing, in the real topology: a PostgreSQL reachable only on an
// internal:true network, with no published port for the host to dial. A
// host-process pgbench cannot reach it at all; pgbench run in a container
// joining the database's own network namespace can, against the caller's
// own -db-url with only its host replaced by that namespace's loopback.
func TestPgbenchRunner_ReachesInternalOnlyPostgresViaContainerNamespace(t *testing.T) {
	requireBinary(t, "docker")
	suffix := fmt.Sprintf("tortureu-dbnetns-%d", time.Now().UnixNano()%1e6)
	netName := internalNetwork(t, suffix+"-net")
	dbName := suffix + "-db"

	if out, err := exec.Command("docker", "run", "-d", "--rm", "--name", dbName, "--network", netName,
		"-e", "POSTGRES_PASSWORD=torturepw", "-e", "POSTGRES_USER=tortureu", "-e", "POSTGRES_DB=torturedb",
		"postgres:16-alpine").CombinedOutput(); err != nil {
		t.Skipf("could not start a postgres container on an internal network (%v): %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", dbName).Run() })
	requireNoPublishedPort(t, dbName)

	deadline := time.Now().Add(90 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if err := exec.Command("docker", "exec", dbName, "psql", "-U", "tortureu", "-d", "torturedb", "-c", "select 1").Run(); err == nil {
			ready = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ready {
		t.Skip("postgres container never became ready")
	}

	// The caller's address: the compose-service host the SUT's own
	// DATABASE_URL would name, since no port exists to publish.
	dsn := fmt.Sprintf("postgresql://tortureu:torturepw@%s:5432/torturedb", dbName)

	// The bug this fixes, confirmed rather than assumed: from the host,
	// this database is unreachable.
	if _, err := (PgbenchRunner{}).Start(dsn, 3*time.Second); err == nil {
		t.Fatal("a host-process pgbench reached an internal-only database — this test no longer exercises the isolation it exists for")
	}

	h, err := (PgbenchRunner{}).InNamespaceOf(dbName).Start(dsn, 5*time.Second)
	if err != nil {
		t.Fatalf("pgbench could not reach an internal-only database from its own network namespace: %v", err)
	}
	time.Sleep(7 * time.Second)
	res := h.Stop()
	if res.Err != nil {
		t.Fatalf("namespace-joined pgbench reported a tool error: %v", res.Err)
	}
	if res.TPS <= 0 || res.Transactions <= 0 {
		t.Fatalf("no throughput against an internal-only database: %+v", res)
	}
	t.Logf("pgbench against an internal-only postgres: %.1f tps, %d transactions", res.TPS, res.Transactions)

	// Nothing may outlive the run (R-EXE-5): Stop must leave no container.
	out, _ := exec.Command("docker", "ps", "-a", "--filter", "name=tortureu-pgbench-", "--format", "{{.Names}}").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("Stop left pgbench containers behind: %s", out)
	}
}

// spec: R-EXE-27
//
// The real thing: a SUT reachable only on an internal:true network, with no
// published port, fuzzed by schemathesis joining that SUT's own network
// namespace — pointed at target.base_url unchanged, which is exactly the
// address k6 uses from inside the same namespace.
func TestSchemathesisRunner_ReachesInternalOnlySUTViaContainerNamespace(t *testing.T) {
	requireBinary(t, "docker")
	suffix := fmt.Sprintf("tortureu-fuzznetns-%d", time.Now().UnixNano()%1e6)
	netName := internalNetwork(t, suffix+"-net")
	sutName := suffix + "-sut"

	if out, err := exec.Command("docker", "run", "-d", "--rm", "--name", sutName, "--network", netName,
		"nginx:alpine").CombinedOutput(); err != nil {
		t.Skipf("could not start an nginx SUT on an internal network (%v): %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", sutName).Run() })
	requireNoPublishedPort(t, sutName)

	// A SUT with an operation that genuinely breaks, so the fuzz pass has a
	// real finding to report rather than proving only that it connected.
	conf := "server {\n listen 80;\n location /ok { return 200 \"ok\"; }\n location /boom { return 500 \"boom\"; }\n}\n"
	if out, err := exec.Command("docker", "exec", sutName, "sh", "-c",
		"printf '%s' "+shellQuote(conf)+" > /etc/nginx/conf.d/default.conf && nginx -s reload").CombinedOutput(); err != nil {
		t.Skipf("could not configure the nginx SUT (%v): %s", err, out)
	}
	time.Sleep(time.Second)

	dir := t.TempDir()
	spec := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(spec, []byte(fuzzTestSpec), 0o644); err != nil {
		t.Fatal(err)
	}

	// target.base_url as it is written for a container-namespace-joined
	// load generator: the SUT's own port on its own loopback. Unreachable
	// from the host, which is the whole problem.
	baseURL := "http://localhost:80"
	h, err := (SchemathesisRunner{MaxExamples: 3, Dir: dir}).InNamespaceOf(sutName).Start(spec, baseURL, 3*time.Minute)
	if err != nil {
		t.Fatalf("schemathesis could not start against an internal-only SUT: %v", err)
	}
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) && !procExited(h) {
		time.Sleep(200 * time.Millisecond)
	}
	res := h.Stop()

	if res.Err != nil {
		t.Fatalf("fuzzing an internal-only SUT was reported as a tool error: %v", res.Err)
	}
	if len(res.Failures) == 0 {
		t.Fatal("no findings against an internal-only SUT whose /boom 500s — the fuzzer never reached it")
	}
	for _, f := range res.Failures {
		t.Logf("fuzz failure: %s -> %s", f.Operation, f.Detail)
	}

	out, _ := exec.Command("docker", "ps", "-a", "--filter", "name=tortureu-fuzz-", "--format", "{{.Names}}").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("Stop left fuzz containers behind: %s", out)
	}
}

// shellQuote wraps s in single quotes for `sh -c`, escaping any it contains.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
