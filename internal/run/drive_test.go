package run

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/detect"
	"github.com/jd316/TortureU/internal/verdict"
)

// fakeDBLoad stands in for PgbenchRunner: it records when Start and Stop
// were called (so a test can prove the lifecycle binds to k6's phase clock,
// R-EXE-26) without needing a live database.
type fakeDBLoad struct {
	started   atomic.Bool
	stopCalls atomic.Int32
	startErr  error
	result    DBLoadResult
}

func (f *fakeDBLoad) Start(dsn string, max time.Duration) (DBLoadHandle, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.started.Store(true)
	return f, nil
}

func (f *fakeDBLoad) Stop() DBLoadResult {
	f.stopCalls.Add(1)
	return f.result
}

// fakeFuzzer stands in for SchemathesisRunner.
type fakeFuzzer struct {
	started   atomic.Bool
	stopCalls atomic.Int32
	startErr  error
	result    FuzzResult
}

func (f *fakeFuzzer) Start(specPath, baseURL string, max time.Duration) (FuzzHandle, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.started.Store(true)
	return f, nil
}

func (f *fakeFuzzer) Stop() FuzzResult {
	f.stopCalls.Add(1)
	return f.result
}

func postgresSystem() detect.System {
	return detect.System{
		Deps: []detect.Dep{{Name: "postgres", Type: "postgresql", Address: "postgres:5432"}},
	}
}

func openapiSystem() detect.System {
	sys := detect.System{}
	sys.Coverage.OpenAPI = true
	return sys
}

func waitForDrive(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// spec: R-EXE-26
func TestRun_DBLoadWithNoPostgresDependencyRefusesLoudly(t *testing.T) {
	reset := &fakeResetter{}
	load := &fakeLoadRunner{handle: newFakeLoadHandle()}
	v := Run(minimalConfig(), detect.System{}, Deps{
		Reset:    reset,
		Topology: &fakeTopology{},
		Load:     load,
		Applier:  &fakeApplier{},
		DBLoad:   &fakeDBLoad{},
	}, Options{DBLoad: true, DBURL: "postgresql://u:p@h:5432/d"})

	if v.Status != verdict.StatusError {
		t.Fatalf("status = %q, want error (a --db-load with no postgres trigger must fail loudly, never no-op)", v.Status)
	}
	if !strings.Contains(v.Error, "postgresql") {
		t.Fatalf("error %q does not name the absent trigger condition", v.Error)
	}
	// The refusal must land before the run perturbs anything.
	if reset.called || load.called {
		t.Fatalf("refusal happened after reset(%v)/load(%v) — R-EXE-26 requires it before either", reset.called, load.called)
	}
}

// spec: R-EXE-26
func TestRun_DBLoadWithoutDSNRefusesAndNeverGuessesCredentials(t *testing.T) {
	db := &fakeDBLoad{}
	v := Run(minimalConfig(), postgresSystem(), Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: newFakeLoadHandle()},
		Applier:  &fakeApplier{},
		DBLoad:   db,
	}, Options{DBLoad: true})

	if v.Status != verdict.StatusError {
		t.Fatalf("status = %q, want error", v.Status)
	}
	if !strings.Contains(v.Error, "-db-url") {
		t.Fatalf("error %q does not name the -db-url flag", v.Error)
	}
	if db.started.Load() {
		t.Fatal("pgbench was started with no connection string — credentials must never be guessed")
	}
}

// spec: R-EXE-26
func TestRun_DBLoadStartsOnFirstPhaseMarkerAndStopsWhenLoadEnds(t *testing.T) {
	handle := newFakeLoadHandle()
	db := &fakeDBLoad{result: DBLoadResult{TPS: 476.8, Transactions: 2374, Clients: 8, DurationS: 5}}

	done := make(chan *verdict.Verdict, 1)
	go func() {
		done <- Run(minimalConfig(), postgresSystem(), Deps{
			Reset:    &fakeResetter{},
			Topology: &fakeTopology{},
			Load:     &fakeLoadRunner{handle: handle},
			Applier:  &fakeApplier{},
			DBLoad:   db,
		}, Options{DBLoad: true, DBURL: "postgresql://u:p@h:5432/d"})
	}()

	// Before any marker, k6 has not announced a phase, so nothing may be
	// driving the database yet (R-EXE-8: the clock is k6's).
	time.Sleep(150 * time.Millisecond)
	if db.started.Load() {
		t.Fatal("db load started before k6 announced its first phase")
	}

	handle.markers <- PhaseMarker{Phase: "peak", At: time.Now()}
	waitForDrive(t, "db load to start on the first phase marker", func() bool { return db.started.Load() })

	close(handle.markers)
	handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}

	v := <-done
	if db.stopCalls.Load() == 0 {
		t.Fatal("db load was never stopped when the load ended")
	}
	art, _ := v.Artifacts["db_load"].(map[string]any)
	if art == nil {
		t.Fatalf("verdict carries no db_load artifact: %#v", v.Artifacts)
	}
	if art["tps"] != 476.8 {
		t.Fatalf("db_load artifact tps = %v, want 476.8", art["tps"])
	}
}

// spec: R-EXE-5
func TestRun_DBLoadIsTornDownOnPanic(t *testing.T) {
	cfg := minimalConfig()
	cfg.Assert = append(cfg.Assert, config.AssertEntry{"promql": "up == 1"})
	handle := newFakeLoadHandle()
	db := &fakeDBLoad{}

	go func() {
		handle.markers <- PhaseMarker{Phase: "peak", At: time.Now()}
		waitFor2(func() bool { return db.started.Load() })
		close(handle.markers)
		handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}
	}()

	v := Run(cfg, postgresSystem(), Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: handle},
		Applier:  &fakeApplier{},
		DBLoad:   db,
		Prom:     panickingProm{},
	}, Options{DBLoad: true, DBURL: "postgresql://u:p@h:5432/d"})

	if v.Status != verdict.StatusError || !strings.Contains(v.Error, "panic") {
		t.Fatalf("status=%q error=%q, want a panic reported as error", v.Status, v.Error)
	}
	if db.stopCalls.Load() == 0 {
		t.Fatal("a panic left pgbench running against the developer's database (R-EXE-5)")
	}
}

// waitFor2 is waitFor without a *testing.T, for use inside a goroutine.
func waitFor2(cond func() bool) {
	deadline := time.Now().Add(3 * time.Second)
	for !cond() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

type panickingProm struct{}

func (panickingProm) Query(expr string) (bool, string, error) { panic("boom") }

// spec: R-EXE-26
func TestRun_DBLoadFailureIsToolErrorNotSUTFailure(t *testing.T) {
	handle := newFakeLoadHandle()
	db := &fakeDBLoad{result: DBLoadResult{Err: errors.New("connection to server failed: password authentication failed")}}

	go func() {
		handle.markers <- PhaseMarker{Phase: "peak", At: time.Now()}
		waitFor2(func() bool { return db.started.Load() })
		close(handle.markers)
		handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}
	}()

	v := Run(minimalConfig(), postgresSystem(), Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: handle},
		Applier:  &fakeApplier{},
		DBLoad:   db,
	}, Options{DBLoad: true, DBURL: "postgresql://u:p@h:5432/d"})

	if v.Status != verdict.StatusError {
		t.Fatalf("status = %q, want error: pgbench failing to run is TortureU failing (R-VER-2)", v.Status)
	}
	if !strings.Contains(v.Error, "authentication") {
		t.Fatalf("error %q loses pgbench's own reason", v.Error)
	}
}

// spec: R-EXE-27
func TestRun_FuzzWithoutOpenAPITriggerRefusesLoudly(t *testing.T) {
	reset := &fakeResetter{}
	cfg := minimalConfig()
	cfg.Target.OpenAPI = "openapi.yaml"
	v := Run(cfg, detect.System{}, Deps{
		Reset:    reset,
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: newFakeLoadHandle()},
		Applier:  &fakeApplier{},
		Fuzz:     &fakeFuzzer{},
	}, Options{Fuzz: true})

	if v.Status != verdict.StatusError {
		t.Fatalf("status = %q, want error", v.Status)
	}
	if !strings.Contains(v.Error, "spec:openapi") {
		t.Fatalf("error %q does not name the absent trigger condition", v.Error)
	}
	if reset.called {
		t.Fatal("refusal happened after reset — R-EXE-27 requires it before")
	}
}

// spec: R-EXE-27
func TestRun_FuzzWithoutSpecPathRefusesRatherThanGuessing(t *testing.T) {
	fz := &fakeFuzzer{}
	v := Run(minimalConfig(), openapiSystem(), Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: newFakeLoadHandle()},
		Applier:  &fakeApplier{},
		Fuzz:     fz,
	}, Options{Fuzz: true})

	if v.Status != verdict.StatusError {
		t.Fatalf("status = %q, want error", v.Status)
	}
	if !strings.Contains(v.Error, "target.openapi") || !strings.Contains(v.Error, "-fuzz-spec") {
		t.Fatalf("error %q does not name where the spec path must come from", v.Error)
	}
	if fz.started.Load() {
		t.Fatal("fuzzer started with no spec — the document must never be guessed")
	}
}

// runFuzz drives one fuzz run to completion with the given fuzzer result
// and returns the verdict.
func runFuzz(t *testing.T, cfg *config.Config, fz *fakeFuzzer) *verdict.Verdict {
	t.Helper()
	handle := newFakeLoadHandle()
	go func() {
		handle.markers <- PhaseMarker{Phase: "peak", At: time.Now()}
		waitFor2(func() bool { return fz.started.Load() })
		close(handle.markers)
		handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}
	}()
	return Run(cfg, openapiSystem(), Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: handle},
		Applier:  &fakeApplier{},
		Fuzz:     fz,
	}, Options{Fuzz: true, FuzzSpec: "openapi.yaml"})
}

// spec: R-EXE-27
func TestRun_FuzzFailuresAreFindingsNotToolErrors(t *testing.T) {
	fz := &fakeFuzzer{result: FuzzResult{Failures: []FuzzFailure{
		{Operation: "GET /orders/{id}", Detail: "Server error [500]"},
	}}}
	v := runFuzz(t, minimalConfig(), fz)

	if v.Status != verdict.StatusFail {
		t.Fatalf("status = %q (error = %q), want fail: a fuzzer finding a 500 is a result, not TortureU failing (R-VER-2)", v.Status, v.Error)
	}
	if fz.stopCalls.Load() == 0 {
		t.Fatal("fuzzer was never stopped when the load ended")
	}
	var found *verdict.Finding
	for i := range v.Findings {
		if strings.Contains(v.Findings[i].Broke.Assertion, "GET /orders/{id}") {
			found = &v.Findings[i]
		}
	}
	if found == nil {
		t.Fatalf("no finding for the fuzz failure: %#v", v.Findings)
	}
	if found.ID == "" {
		t.Fatal("fuzz finding carries no ID")
	}
	if found.Confidence != verdict.Correlated {
		t.Fatalf("confidence = %q, want correlated (no fault was declared, so the fuzzer's own request is the sole candidate cause)", found.Confidence)
	}
	if verdict.ExitCode(*v) != 1 {
		t.Fatalf("exit code = %d, want 1", verdict.ExitCode(*v))
	}
}

// spec: R-EXE-27
func TestRun_FuzzFindingIsAmbiguousWhenAFaultWasDeclared(t *testing.T) {
	cfg := minimalConfig()
	cfg.Faults = []config.Fault{{
		Name: "pg_slow", At: "t=0s", Target: "checkout-api",
		Verb: "pause", Inject: map[string]any{"pause": true},
	}}
	fz := &fakeFuzzer{result: FuzzResult{Failures: []FuzzFailure{{Operation: "GET /x", Detail: "Server error [500]"}}}}
	v := runFuzz(t, cfg, fz)

	for _, f := range v.Findings {
		if strings.Contains(f.Broke.Assertion, "GET /x") && f.Confidence != verdict.Ambiguous {
			t.Fatalf("confidence = %q, want ambiguous when a fault is a second candidate cause (R-VER-3)", f.Confidence)
		}
	}
}

// spec: R-EXE-27
func TestRun_FuzzCutShortByLoadEndingWarnsRatherThanReportingClean(t *testing.T) {
	fz := &fakeFuzzer{result: FuzzResult{CutShort: true}}
	v := runFuzz(t, minimalConfig(), fz)

	warnings, _ := v.Artifacts["warnings"].([]string)
	joined := strings.Join(warnings, " | ")
	if !strings.Contains(joined, "cut short") {
		t.Fatalf("warnings %q do not report the fuzz run was cut short", joined)
	}
}

// spec: R-EXE-27
func TestRun_FuzzToolFailureIsStatusError(t *testing.T) {
	fz := &fakeFuzzer{result: FuzzResult{Err: errors.New("all cases failed with a network error")}}
	v := runFuzz(t, minimalConfig(), fz)

	if v.Status != verdict.StatusError {
		t.Fatalf("status = %q, want error: a schemathesis that could not run at all is TortureU failing", v.Status)
	}
}

// spec: R-CLI-5
func TestPgbenchRunner_MissingBinaryCarriesInstallHint(t *testing.T) {
	// Both routes absent, not just the binary: Docker alone is enough to
	// run pgbench's own image in the database's network namespace
	// (R-EXE-26's Reach rule), so refusing over a missing binary while
	// Docker is present would refuse a machine that can in fact do the job.
	err := PgbenchRunner{Bin: "tortureu-no-such-pgbench", DockerBin: "tortureu-no-such-docker"}.Preflight()
	if err == nil {
		t.Fatal("Preflight passed with neither pgbench nor docker on PATH")
	}
	if !strings.Contains(err.Error(), "tortureu-no-such-docker") {
		t.Errorf("error %q does not name the container route as the other way to get there", err)
	}
	if err := (PgbenchRunner{Bin: "tortureu-no-such-pgbench", DockerBin: "sh"}).Preflight(); err != nil {
		t.Errorf("Preflight refused a machine with no pgbench but a working container route: %v", err)
	}
	if !strings.Contains(err.Error(), "install") {
		t.Fatalf("error %q carries no install hint (R-CLI-5)", err)
	}
}

// spec: R-CLI-5
func TestSchemathesisRunner_MissingBinaryCarriesInstallHint(t *testing.T) {
	err := SchemathesisRunner{Bin: "tortureu-no-such-st", DockerBin: "tortureu-no-such-docker"}.Preflight()
	if err == nil {
		t.Fatal("Preflight passed with neither schemathesis nor docker on PATH")
	}
	if err := (SchemathesisRunner{Bin: "tortureu-no-such-st", DockerBin: "sh"}).Preflight(); err != nil {
		t.Errorf("Preflight refused a machine with no schemathesis but a working container route: %v", err)
	}
	if !strings.Contains(err.Error(), "install") {
		t.Fatalf("error %q carries no install hint (R-CLI-5)", err)
	}
}

// spec: R-EXE-26
func TestRun_DBLoadPreflightsTheBinaryBeforeResetting(t *testing.T) {
	reset := &fakeResetter{}
	v := Run(minimalConfig(), postgresSystem(), Deps{
		Reset:    reset,
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: newFakeLoadHandle()},
		Applier:  &fakeApplier{},
		DBLoad:   PgbenchRunner{Bin: "tortureu-no-such-pgbench", DockerBin: "tortureu-no-such-docker"},
	}, Options{DBLoad: true, DBURL: "postgresql://u:p@h:5432/d"})

	if v.Status != verdict.StatusError || !strings.Contains(v.Error, "install") {
		t.Fatalf("status=%q error=%q, want an install hint before anything ran", v.Status, v.Error)
	}
	if reset.called {
		t.Fatal("reset ran before the missing-binary refusal")
	}
}

// spec: R-EXE-26
func TestDriveDuration_BoundsPgbenchByTheDeclaredLoadProfile(t *testing.T) {
	cfg := minimalConfig()
	cfg.Load.Stages = []config.Stage{
		{Phase: "ramp", To: "50rps", Over: "30s"},
		{Phase: "peak", Hold: "50rps", For: "2m"},
	}
	got := driveDuration(cfg)
	if want := 30*time.Second + 2*time.Minute + driveDurationSlack; got != want {
		t.Fatalf("driveDuration = %v, want %v (declared profile plus slack)", got, want)
	}
}

// spec: R-VER-11
//
// The verdict must state the repo's observability coverage and its
// confidence ceiling. This was never populated: the field existed in
// VERDICT.md §1 and internal/verdict, and internal/run never wrote it, so
// every verdict rendered the zero value — false for a repo with tracing,
// and silent about the ceiling for a repo without.
func TestRun_VerdictCarriesDetectedObservabilityCoverage(t *testing.T) {
	sys := detect.System{Obs: detect.Obs{Traces: true, Metrics: true, MaxConfidence: "caused"}}
	handle := newFakeLoadHandle()
	handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}
	close(handle.markers)

	v := Run(minimalConfig(), sys, Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: handle},
		Applier:  &fakeApplier{},
	}, Options{})

	if !v.Observability.Traces || !v.Observability.Metrics || v.Observability.Logs {
		t.Errorf("observability coverage did not reach the verdict: %+v", v.Observability)
	}
	if v.Observability.MaxConfidence != verdict.Caused {
		t.Errorf("max_confidence = %q, want caused", v.Observability.MaxConfidence)
	}
}

// spec: R-VER-11
//
// The floor case is the one that matters most: a repo with no observability
// infrastructure at all still has a ceiling (correlated), and it must be
// stated rather than omitted — max_confidence is `omitempty`, so an empty
// value vanishes from the JSON for exactly the repos that needed telling.
func TestRun_VerdictStatesTheConfidenceCeilingEvenWithNoObservability(t *testing.T) {
	sys := detect.System{Obs: detect.Obs{MaxConfidence: "correlated"}}
	handle := newFakeLoadHandle()
	handle.done <- LoadResult{SummaryJSON: []byte(`{"metrics":{}}`)}
	close(handle.markers)

	v := Run(minimalConfig(), sys, Deps{
		Reset:    &fakeResetter{},
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: handle},
		Applier:  &fakeApplier{},
	}, Options{})

	if v.Observability.MaxConfidence != verdict.Correlated {
		t.Errorf("max_confidence = %q, want correlated (the floor, never empty)", v.Observability.MaxConfidence)
	}
}

// spec: R-VER-4
//
// Ruby and Java clients are detected now (Gemfile/pom.xml), and reached the
// verdict as candidates with no knobs at all — a candidate naming a library
// and nothing actionable is barely better than none.
func TestKnobsFor_CoversRubyAndJavaClients(t *testing.T) {
	cases := map[string]string{
		"pg":                             "checkout_timeout",
		"mysql2":                         "checkout_timeout",
		"redis":                          "reconnect_attempts",
		"org.postgresql:postgresql":      "socketTimeout",
		"redis.clients:jedis":            "JedisPoolConfig.maxTotal",
		"org.apache.kafka:kafka-clients": "request.timeout.ms",
		"com.zaxxer:HikariCP":            "maximumPoolSize",
	}
	for client, want := range cases {
		knobs := knobsFor(client)
		if len(knobs) == 0 {
			t.Errorf("%s: no knobs at all", client)
			continue
		}
		found := false
		for _, k := range knobs {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: knobs %v do not include %q", client, knobs, want)
		}
	}
	// The Ruby gem "pg" must not be matched as a substring of Go's
	// "github.com/jackc/pgx/v5" — a bare two-letter name inside a substring
	// table is exactly how a wrong knob list reaches the wrong library.
	if got := knobsFor("github.com/jackc/pgx/v5"); len(got) == 0 || got[0] != "MaxConns" {
		t.Errorf("pgx knobs = %v, want the pgx list", got)
	}
}

// netnsDBLoad is fakeDBLoad plus the namespace-binding hook the real
// PgbenchRunner implements, so coDrivers' own wiring is provable without a
// live daemon (the real join is proven in drive_real_test.go).
type netnsDBLoad struct {
	fakeDBLoad
	netns string
}

func (f *netnsDBLoad) InNamespaceOf(container string) DBLoadRunner {
	f.netns = container
	return f
}

// netnsFuzzer is fakeFuzzer plus the same hook.
type netnsFuzzer struct {
	fakeFuzzer
	netns string
}

func (f *netnsFuzzer) InNamespaceOf(container string) Fuzzer {
	f.netns = container
	return f
}

// spec: R-EXE-26
// spec: R-EXE-27
//
// TBD-12: the co-driven tools must be told which container's network
// namespace to join before they start, or they cannot reach a database/SUT
// R-DC2-3 put on an internal-only network. Each is bound to the container
// its *own* address names — the DB to the compose service `-db-url`'s host
// names, the fuzzer to `target.service`, the same anchor the load path
// (run.go's SetSUTContainer wiring) already uses — never to a container
// inferred from the compose file.
func TestCoDrivers_BindEachRunnerToTheContainerItsOwnAddressNames(t *testing.T) {
	var asked []string
	withFakeSUTDiscovery(t, func(service string) (string, error) {
		asked = append(asked, service)
		return service + "-container-1", nil
	})

	cfg := minimalConfig()
	cfg.Target.OpenAPI = "openapi.yaml"
	db := &netnsDBLoad{}
	fz := &netnsFuzzer{}
	c := newCoDrivers(cfg, Deps{DBLoad: db, Fuzz: fz}, Options{
		DBLoad: true, DBURL: "postgresql://u:p@orders-db:5432/orders", Fuzz: true,
	})
	c.start()

	if db.netns != "orders-db-container-1" {
		t.Errorf("DB load bound to namespace %q, want the container of the compose service its own -db-url names", db.netns)
	}
	if fz.netns != "checkout-api-container-1" {
		t.Errorf("fuzzer bound to namespace %q, want the SUT container the load generator joins", fz.netns)
	}
	if len(asked) != 2 || asked[0] != "orders-db" || asked[1] != "checkout-api" {
		t.Errorf("discovery asked for %v, want [orders-db checkout-api]", asked)
	}
	if !db.started.Load() || !fz.started.Load() {
		t.Error("namespace binding must not stop the runners from starting")
	}
}

// spec: R-EXE-26
//
// The only address translation R-EXE-26 permits: the host becomes the
// container's own loopback and *nothing else changes* — the port above all,
// since an internal-only container publishes none and the caller's port is
// therefore the only port its server can be listening on.
func TestDSNInNamespace_RewritesOnlyTheHost(t *testing.T) {
	got, ok := dsnInNamespace("postgresql://tortureu:s3cr%2Ft@orders-db:6543/orders?sslmode=disable")
	if !ok {
		t.Fatal("a plain conninfo URI was not rewritable")
	}
	if want := "postgresql://tortureu:s3cr%2Ft@127.0.0.1:6543/orders?sslmode=disable"; got != want {
		t.Errorf("dsnInNamespace = %q, want %q", got, want)
	}
	// A DSN with no port at all still names one implicitly, and it is
	// PostgreSQL's, not ours to invent.
	if got, _ := dsnInNamespace("postgres://u@db/d"); got != "postgres://u@127.0.0.1:5432/d" {
		t.Errorf("portless DSN = %q, want the default 5432 made explicit", got)
	}
	// Anything this rule cannot rewrite must say so rather than be guessed
	// at: keyword conninfo, and a DSN that is not a URI at all.
	for _, bad := range []string{"host=orders-db port=5432 dbname=orders", "orders", ""} {
		if _, ok := dsnInNamespace(bad); ok {
			t.Errorf("dsnInNamespace(%q) claimed to rewrite a form this rule does not cover", bad)
		}
	}
}
