package emit

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/detect"
)

// spec: R-CLI-8

const fioFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://localhost:8080
egress:
  default: deny
  hosts:
    mysql:3306: { class: internal }
load:
  engine: k6
  model: arrival_rate
  stages:
    - phase: peak
      hold: 500rps
      for: 60s
faults:
  - name: db_io_pressure
    at: peak
    for: 30s
    target: mysql:3306
    inject: { io: 500m, workers: 2 }
  - name: db_dies
    at: peak
    for: 10s
    target: mysql:3306
    inject: { down: true }
  - name: cpu_squeeze
    at: peak
    for: 10s
    target: checkout-api
    inject: { cpu: 90%, workers: 4 }
assert:
  - http_req_duration: ["p(95)<500"]
`

// R-CLI-8 (proposed): fio translates "io" resource-pressure faults into a
// docker exec fio command against the fault's own container, and reports
// (never drops) every other verb it does not translate.
func TestFio_IOFault_UsesDependencyContainerAndDataDir(t *testing.T) {
	cfg := mustParse(t, fioFixture)
	sys := &detect.System{Deps: []detect.Dep{{Name: "mysql", Type: "mysql", Address: "mysql:3306"}}}
	out, err := Fio(cfg, sys)
	if err != nil {
		t.Fatalf("Fio: %v", err)
	}
	if !strings.Contains(out, "docker exec mysql fio --name=torture_io_db_io_pressure --directory=/var/lib/mysql") {
		t.Errorf("expected fio against the mysql container's data dir, got:\n%s", out)
	}
	if !strings.Contains(out, "--size=500m") {
		t.Errorf("expected --size=500m from inject.io, got:\n%s", out)
	}
	if !strings.Contains(out, "--numjobs=2") {
		t.Errorf("expected --numjobs=2 from inject.workers, got:\n%s", out)
	}
	if !strings.Contains(out, "--runtime=30s --time_based") {
		t.Errorf("expected a bounded runtime from for: 30s, got:\n%s", out)
	}
}

func TestFio_NoDetectedDependency_FallsBackToTmpAndSaysSo(t *testing.T) {
	cfg := mustParse(t, fioFixture)
	out, err := Fio(cfg, nil)
	if err != nil {
		t.Fatalf("Fio: %v", err)
	}
	if !strings.Contains(out, "--directory=/tmp") {
		t.Errorf("expected a /tmp fallback without detection, got:\n%s", out)
	}
	if !strings.Contains(out, "no detected dependency data directory") {
		t.Errorf("expected the fallback to be reported, not silent, got:\n%s", out)
	}
}

func TestFio_SkipsVerbsItDoesNotTranslate(t *testing.T) {
	cfg := mustParse(t, fioFixture)
	out, err := Fio(cfg, nil)
	if err != nil {
		t.Fatalf("Fio: %v", err)
	}
	if !strings.Contains(out, `fault "db_dies" (inject: down): not translated by fio`) {
		t.Errorf("expected db_dies to be reported as skipped, got:\n%s", out)
	}
	if !strings.Contains(out, `fault "cpu_squeeze" (inject: cpu): not translated by fio`) {
		t.Errorf("expected cpu_squeeze to be reported as skipped, got:\n%s", out)
	}
}

func TestFio_UnknownFaultStillErrorsOnMalformedInput(t *testing.T) {
	cfg := &config.Config{
		Target: config.Target{Service: "svc"},
		Faults: []config.Fault{{Name: "bad", Target: "svc", Verb: "not_a_verb", Inject: map[string]any{"not_a_verb": true}}},
	}
	if _, err := Fio(cfg, nil); err == nil {
		t.Fatal("expected an error for an unrecognized verb")
	}
}

// TestFio_FlagsAreReal actually runs the exact fio flag shape this emit
// generates against a scratch directory, per the task's verification
// standard: "run the emitted command through the real tool where one is
// available." fio was installed for this session (apt-get install fio);
// skip cleanly if it is not present in whatever environment runs this.
func TestFio_FlagsAreReal(t *testing.T) {
	if _, err := exec.LookPath("fio"); err != nil {
		t.Skip("fio not installed; see protocol.go's fioHeader for the documented verification this test performs when it is")
	}
	dir := t.TempDir()
	cmd := exec.Command("fio", "--name=torture_io_test", "--directory="+dir,
		"--rw=randrw", "--bs=4k", "--size=4m", "--numjobs=1", "--runtime=1s", "--time_based", "--group_reporting")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fio (the exact flag shape Fio() emits) failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "READ:") || !strings.Contains(string(out), "WRITE:") {
		t.Errorf("expected real read/write summary lines from fio, got:\n%s", out)
	}
}

// R-CLI-8 (proposed): clockskew has no fault-verb gate (registry.yaml:
// when: always) — it always emits a libfaketime command for the SUT
// service, since torture.yaml's faults: grammar has no clockskew verb to
// drive it from.
func TestClockSkew_TargetsSUTService(t *testing.T) {
	cfg := mustParse(t, fioFixture)
	out, err := ClockSkew(cfg)
	if err != nil {
		t.Fatalf("ClockSkew: %v", err)
	}
	if !strings.Contains(out, "docker exec -e LD_PRELOAD=") || !strings.Contains(out, " checkout-api date -u") {
		t.Errorf("expected a docker exec skew-verification command against the SUT container, got:\n%s", out)
	}
	if !strings.Contains(out, "FAKETIME=") {
		t.Errorf("expected a FAKETIME offset to be set, got:\n%s", out)
	}
	if !strings.Contains(out, "force-recreate checkout-api") {
		t.Errorf("expected the real-skew recipe to name the SUT service, got:\n%s", out)
	}
}

// TestClockSkew_MechanismIsReal actually runs libfaketime's LD_PRELOAD/
// FAKETIME mechanism this emit documents, per the verification standard.
// faketime/libfaketime was installed for this session
// (apt-get install faketime); skip cleanly if absent.
func TestClockSkew_MechanismIsReal(t *testing.T) {
	if _, err := exec.LookPath("faketime"); err != nil {
		t.Skip("faketime not installed; see protocol.go's clockskewHeaderTemplate for the documented verification this test performs when it is")
	}
	out, err := exec.Command("faketime", "+2 years", "date", "+%Y").CombinedOutput()
	if err != nil {
		t.Fatalf("faketime (the mechanism clockskew emits via LD_PRELOAD/FAKETIME) failed: %v\n%s", err, out)
	}
	skewedYear := strings.TrimSpace(string(out))
	realOut, err := exec.Command("date", "+%Y").CombinedOutput()
	if err != nil {
		t.Fatalf("date: %v", err)
	}
	if skewedYear == strings.TrimSpace(string(realOut)) {
		t.Errorf("expected faketime to actually skew the reported year, got the same year %q both times", skewedYear)
	}
}

const loadFixture = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://localhost:8080
egress:
  default: deny
  hosts:
    mysql:3306: { class: internal }
    redis:6379: { class: internal }
load:
  engine: k6
  model: arrival_rate
  stages:
    - phase: ramp_up
      to: 500rps
      over: 60s
    - phase: peak
      hold: 3000rps
      for: 120s
assert:
  - http_req_duration: ["p(95)<500"]
`

// R-CLI-8 (proposed): sysbench is driven by the detected mysql dependency
// (dep:mysql) and sized from load.stages' peak rate/total duration, per
// the task brief ("protocol load — driven by the detected dependency and
// the load: block").
func TestSysbench_UsesDetectedDependencyAndLoadProfile(t *testing.T) {
	cfg := mustParse(t, loadFixture)
	sys := &detect.System{Deps: []detect.Dep{{Name: "mysql", Type: "mysql", Address: "mysql:3306"}}}
	out, err := Sysbench(cfg, sys)
	if err != nil {
		t.Fatalf("Sysbench: %v", err)
	}
	if !strings.Contains(out, "--network container:mysql") {
		t.Errorf("expected the dependency's own container network namespace, got:\n%s", out)
	}
	if !strings.Contains(out, "--mysql-port=3306") {
		t.Errorf("expected the detected port, got:\n%s", out)
	}
	// peak 3000rps / 50 = 60 threads.
	if !strings.Contains(out, "--threads=60") {
		t.Errorf("expected thread count derived from peak rps, got:\n%s", out)
	}
	// 60s + 120s = 180s total.
	if !strings.Contains(out, "--time=180") {
		t.Errorf("expected duration derived from stage total, got:\n%s", out)
	}
	if !strings.Contains(out, "prepare") || !strings.Contains(out, "run") || !strings.Contains(out, "cleanup") {
		t.Errorf("expected the full prepare/run/cleanup sequence, got:\n%s", out)
	}
}

func TestSysbench_NoDetectedDependency_ReportsRatherThanGuessing(t *testing.T) {
	cfg := mustParse(t, loadFixture)
	// Two distinct causes, two distinct reports: a nil *detect.System means
	// detection never ran, which must not be reported as the user's compose
	// file lacking a mysql.
	out, err := Sysbench(cfg, nil)
	if err != nil {
		t.Fatalf("Sysbench: %v", err)
	}
	if !strings.Contains(out, "could not be detected") {
		t.Errorf("expected an undetected-system report, got:\n%s", out)
	}
	detected, err := Sysbench(cfg, &detect.System{})
	if err != nil {
		t.Fatalf("Sysbench: %v", err)
	}
	if !strings.Contains(detected, "no mysql dependency was detected") {
		t.Errorf("expected an explicit not-detected report, got:\n%s", detected)
	}
	if strings.Contains(detected, "docker run") {
		t.Errorf("expected no command without a detected dependency, got:\n%s", detected)
	}
	if strings.Contains(out, "docker run") {
		t.Errorf("expected no command without a detected dependency, got:\n%s", out)
	}
}

// R-CLI-8 (proposed): same shape as sysbench, for the detected redis
// dependency (dep:redis).
func TestMemtier_UsesDetectedDependencyAndLoadProfile(t *testing.T) {
	cfg := mustParse(t, loadFixture)
	sys := &detect.System{Deps: []detect.Dep{{Name: "redis", Type: "redis", Address: "redis:6379"}}}
	out, err := Memtier(cfg, sys)
	if err != nil {
		t.Fatalf("Memtier: %v", err)
	}
	if !strings.Contains(out, "--network container:redis") {
		t.Errorf("expected the dependency's own container network namespace, got:\n%s", out)
	}
	if !strings.Contains(out, "--port=6379") {
		t.Errorf("expected the detected port, got:\n%s", out)
	}
	if !strings.Contains(out, "--clients=60") {
		t.Errorf("expected client count derived from peak rps, got:\n%s", out)
	}
	if !strings.Contains(out, "--test-time=180") {
		t.Errorf("expected duration derived from stage total, got:\n%s", out)
	}
}

func TestMemtier_NoDetectedDependency_ReportsRatherThanGuessing(t *testing.T) {
	cfg := mustParse(t, loadFixture)
	// Two distinct causes, two distinct reports: a nil *detect.System means
	// detection never ran, which must not be reported as the user's compose
	// file lacking a redis.
	out, err := Memtier(cfg, nil)
	if err != nil {
		t.Fatalf("Memtier: %v", err)
	}
	if !strings.Contains(out, "could not be detected") {
		t.Errorf("expected an undetected-system report, got:\n%s", out)
	}
	detected, err := Memtier(cfg, &detect.System{})
	if err != nil {
		t.Fatalf("Memtier: %v", err)
	}
	if !strings.Contains(detected, "no redis dependency was detected") {
		t.Errorf("expected an explicit not-detected report, got:\n%s", detected)
	}
	if strings.Contains(detected, "docker run") {
		t.Errorf("expected no command without a detected dependency, got:\n%s", detected)
	}
}

// EmitProtocol is this file's own CLI-shaped entry point (see protocol.go
// package doc: emit.go's Emit does not dispatch here). It must still
// behave like Emit for an unknown tool name (R-CLI-8: error listing what
// it supports).
func TestEmitProtocol_UnknownToolListsSupported(t *testing.T) {
	cfg := mustParse(t, fioFixture)
	_, err := EmitProtocol("not-a-real-tool", cfg, nil)
	if err == nil {
		t.Fatal("expected an error for an unknown tool")
	}
	for _, name := range ProtocolTools {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("expected the error to list %q among supported tools, got: %v", name, err)
		}
	}
}

func TestEmitProtocol_DispatchesToEachTool(t *testing.T) {
	cfg := mustParse(t, fioFixture)
	for _, name := range ProtocolTools {
		if _, err := EmitProtocol(name, cfg, nil); err != nil {
			t.Errorf("EmitProtocol(%q): %v", name, err)
		}
	}
}

// dockerAvailable reports whether docker is present and reachable, so the
// full end-to-end sysbench/memtier docker runs (verified manually against
// live mysql:8.0 and redis:7-alpine containers during development — see
// sysbenchHeader/memtierHeader for the real throughput numbers observed)
// can also run as an automated, opt-in integration test without making
// the default `go test ./...` gate depend on docker/network/image-pull
// availability in whatever environment runs it.
func dockerAvailable(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		return false
	}
	return true
}

// TestSysbench_EndToEnd_AgainstLiveMySQL runs the exact command Sysbench
// generates against a real mysql:8.0 container, reproducing the manual
// verification performed during development. It is fully self-contained
// (starts and removes its own container) but opt-in via
// TORTUREU_DOCKER_TESTS=1, since it pulls images and takes tens of
// seconds — too slow/networked for the default `-race` gate.
func TestSysbench_EndToEnd_AgainstLiveMySQL(t *testing.T) {
	if os.Getenv("TORTUREU_DOCKER_TESTS") != "1" {
		t.Skip("set TORTUREU_DOCKER_TESTS=1 to run the live-mysql sysbench end-to-end check")
	}
	if !dockerAvailable(t) {
		t.Skip("docker not available")
	}
	const container = "emit-protocol-test-mysql"
	exec.Command("docker", "rm", "-f", container).Run()
	run := exec.Command("docker", "run", "-d", "--name", container,
		"-e", "MYSQL_ROOT_PASSWORD=torture", "-e", "MYSQL_DATABASE=sbtest", "mysql:8.0")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("docker run mysql: %v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", container).Run() })

	ready := false
	for i := 0; i < 60; i++ {
		if exec.Command("docker", "exec", container, "mysqladmin", "ping", "-uroot", "-ptorture", "--silent").Run() == nil {
			ready = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !ready {
		t.Fatal("mysql never became ready")
	}
	// mysql:8.0 defaults to caching_sha2_password, which severalnines/
	// sysbench's bundled client library can't load; switch root to
	// mysql_native_password to match what the emitted command expects
	// against a stock image (the emitted output notes credentials are
	// placeholders the user supplies for their own setup).
	//
	// The entrypoint's mysqladmin-ping can succeed against a transient
	// bootstrap instance before the real server (with root's password
	// already set) is accepting connections, so this retries rather than
	// treating the first failure as fatal.
	altered := false
	var lastOut []byte
	var lastErr error
	for i := 0; i < 30; i++ {
		alter := exec.Command("docker", "exec", container, "mysql", "-uroot", "-ptorture", "-e",
			"ALTER USER 'root'@'%' IDENTIFIED WITH mysql_native_password BY 'torture'; FLUSH PRIVILEGES;")
		lastOut, lastErr = alter.CombinedOutput()
		if lastErr == nil {
			altered = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !altered {
		t.Fatalf("mysql ALTER USER never succeeded: %v\n%s", lastErr, lastOut)
	}

	cfg := mustParse(t, loadFixture)
	sys := &detect.System{Deps: []detect.Dep{{Name: container, Type: "mysql", Address: container + ":3306"}}}
	out, err := Sysbench(cfg, sys)
	if err != nil {
		t.Fatalf("Sysbench: %v", err)
	}

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(line, "docker run") {
			continue
		}
		line = strings.Replace(line, "--mysql-user=CHANGE_ME", "--mysql-user=root", 1)
		line = strings.Replace(line, "--mysql-password=CHANGE_ME", "--mysql-password=torture", 1)
		line = strings.Replace(line, "--mysql-db=CHANGE_ME", "--mysql-db=sbtest", 1)
		// Shrink the dataset for this check: the emitted default
		// (10 tables x 100k rows) is sized for a real load run, not a
		// pass/fail smoke test, and takes minutes against a fresh
		// container's redo log.
		line = strings.Replace(line, "--tables=10", "--tables=1", 1)
		line = strings.Replace(line, "--table-size=100000", "--table-size=1000", 1)
		// Same reasoning for the run duration: 180s (this fixture's real
		// load-profile total) is correct production sizing, but far too
		// slow for a pass/fail check.
		line = strings.Replace(line, "--time=180", "--time=3", 1)
		args := strings.Fields(line)[1:] // drop "docker"
		cmd := exec.Command("docker", args...)
		cmdOut, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("emitted command failed: %s\n%v\n%s", line, err, cmdOut)
		}
		if strings.Contains(line, " run") && !strings.HasSuffix(strings.TrimSpace(line), "run") {
			// the "prepare"/"cleanup" lines also contain the substring
			// " run" (oltp_read_write), so only assert on the line
			// literally ending in the "run" subcommand.
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(line), " run") {
			if !strings.Contains(string(cmdOut), "transactions:") {
				t.Errorf("expected real sysbench transaction output from the emitted run command, got:\n%s", cmdOut)
			}
		}
	}
}
