package run

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// spec: R-CFG-17
func TestHTTPPromQuerier_NonEmptyResultMeansConditionHolds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != `sum(rate(app_retries_total[30s])) < 100` {
			t.Errorf("query = %q", r.URL.Query().Get("query"))
		}
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}`))
	}))
	defer srv.Close()

	q := HTTPPromQuerier{BaseURL: srv.URL}
	holds, _, err := q.Query(`sum(rate(app_retries_total[30s])) < 100`)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !holds {
		t.Error("holds = false, want true for a non-empty result vector")
	}
}

// spec: R-VER-1
func TestHTTPPromQuerier_HoldingResultReportsActualMeasuredValue(t *testing.T) {
	// VERDICT.md §1's "observed" is a real measured value; a query that
	// holds should report the number Prometheus actually returned, not
	// merely a result count.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"42.5"]}]}}`))
	}))
	defer srv.Close()

	q := HTTPPromQuerier{BaseURL: srv.URL}
	holds, observed, err := q.Query(`some_metric`)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !holds {
		t.Fatal("holds = false, want true")
	}
	if observed != "42.5" {
		t.Errorf("observed = %q, want the actual measured value \"42.5\", not a result count", observed)
	}
}

// spec: R-CFG-17
func TestHTTPPromQuerier_EmptyResultMeansConditionFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	q := HTTPPromQuerier{BaseURL: srv.URL}
	holds, observed, err := q.Query(`orders_total == payments_total`)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if holds {
		t.Error("holds = true, want false for an empty result vector")
	}
	if observed == "" {
		t.Error("observed is empty, want a human-readable rendering of what was measured (R-VER-5)")
	}
}

// spec: R-CFG-17
func TestHTTPPromQuerier_ServerErrorIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"error","error":"bad query"}`))
	}))
	defer srv.Close()

	q := HTTPPromQuerier{BaseURL: srv.URL}
	if _, _, err := q.Query(`invalid{`); err == nil {
		t.Error("Query returned nil error for a Prometheus-reported query failure")
	}
}

// spec: R-CFG-17
//
// TestHTTPPromQuerier_NonEmptyResultMeansConditionHolds (above) already
// proves the out-of-stack case, unchanged by this fix: an httptest.Server
// is always directly reachable from the host, so Query's primary path
// succeeds and the fallback below is never even attempted. This test
// proves the other half an E1 finding identified: R-DC2-3's own topology
// enforcement can put a real Prometheus on the SUT's internal-only
// network, unreachable as a plain host-process HTTP call — the identical
// reachability problem K6Runner already solves for the SUT itself (see
// load.go's package doc comment), one layer over. The container below
// publishes no port at all, standing in for exactly that shape; Query
// must still reach it, by joining its own network namespace rather than
// assuming host reachability.
func TestHTTPPromQuerier_ReachesInStackPrometheusViaContainerNamespace(t *testing.T) {
	dockerAvailable(t)

	name := "promt-" + uniqueSuffix("prom")
	out, err := exec.Command("docker", "run", "-d", "--name", name,
		// discoverSUTContainer (reused, unmodified, for any compose
		// service — not just the SUT) keys off this exact label.
		"--label", "com.docker.compose.service="+name,
		"busybox:1.36", "sh", "-c",
		`mkdir -p /www/api/v1 && printf '{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"1"]}]}}' > /www/api/v1/query && httpd -f -p 9090 -h /www`,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	t.Cleanup(func() { closeHopTransports(); exec.Command("docker", "rm", "-f", name).Run() })

	// Confirm the premise before asserting the fix: no published port
	// means a plain host dial cannot reach it, exactly the failure mode
	// this fix addresses. If this ever starts passing, the test below
	// would no longer be testing what it claims to.
	deadline := waitForContainer(t, name)
	_ = deadline
	if portOut, _ := exec.Command("docker", "port", name).Output(); len(portOut) != 0 {
		t.Fatalf("container unexpectedly has a published port (%s) — this test needs an unpublished one to reproduce the DC-2-isolated shape", portOut)
	}

	q := HTTPPromQuerier{BaseURL: "http://" + name + ":9090"}
	holds, observed, err := q.Query("up")
	if err != nil {
		t.Fatalf("Query: %v — a plain host-process HTTP call cannot reach this container (no published port); Query must fall back to joining its network namespace", err)
	}
	if !holds {
		t.Error("holds = false, want true (the query returned a result)")
	}
	if observed != "1" {
		t.Errorf("observed = %q, want %q", observed, "1")
	}
}

// waitForContainer polls until name's httpd is actually accepting
// connections inside its own namespace, so the test above isn't racing the
// container's own startup — a container-hop request that arrives before
// httpd has bound its port would fail for an unrelated, flaky reason.
func waitForContainer(t *testing.T, name string) bool {
	t.Helper()
	for i := 0; i < 50; i++ {
		if out, err := exec.Command("docker", "exec", name, "wget", "-qO-", "http://localhost:9090/api/v1/query").CombinedOutput(); err == nil && len(out) > 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("container's httpd never became reachable via docker exec")
	return false
}

// spec: R-CFG-17
//
// This is the exact configuration an E1 finding reproduced deterministically
// outside TortureU: BusyBox `nc localhost <port>` resolves "localhost" to
// ::1 first and never falls back to the A record, so any IPv4-only
// listener (Redpanda's Pandaproxy binds 0.0.0.0, as do many real services)
// was unreachable through the tunnel — the caller saw a bare EOF.
// TestHTTPPromQuerier_ReachesInStackPrometheusViaContainerNamespace above
// uses BusyBox httpd, which happens to also accept connections on ::1, so
// it could not have caught this: it would pass identically whether or not
// the v4/v6 fix existed. This test's server binds AF_INET only — genuinely
// unreachable on ::1 — which is the one configuration that actually
// exercises containerHopScript's fallback-across-addresses logic. This is
// the proof E1 asked for: with this fix, the dual-homed
// Prometheus/Redpanda workaround E1 had to restore in its own harness is
// no longer necessary.
func TestHTTPPromQuerier_ReachesIPv4OnlyInStackServiceViaContainerNamespace(t *testing.T) {
	dockerAvailable(t)

	name := "promv4-" + uniqueSuffix("prom")
	script := `import socket
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("0.0.0.0", 9090))
s.listen(5)
body = b'{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"1"]}]}}'
resp = b"HTTP/1.0 200 OK\r\nContent-Type: application/json\r\nContent-Length: " + str(len(body)).encode() + b"\r\n\r\n" + body
while True:
    c, _ = s.accept()
    c.recv(4096)
    c.sendall(resp)
    c.close()
`
	out, err := exec.Command("docker", "run", "-d", "--name", name,
		"--label", "com.docker.compose.service="+name,
		"python:3-alpine", "python3", "-c", script,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	t.Cleanup(func() { closeHopTransports(); exec.Command("docker", "rm", "-f", name).Run() })

	// Confirm the premise: reachable on 127.0.0.1, genuinely not on ::1 —
	// an AF_INET-only socket never listens on ::1 at all, unlike
	// BusyBox httpd's own dual-stack default.
	waitForIPv4Server(t, name)
	if err := exec.Command("docker", "exec", name, "wget", "-qO-", "-T", "1", "http://[::1]:9090/").Run(); err == nil {
		t.Fatal("server unexpectedly answered on ::1 — this test needs a genuinely IPv4-only listener to prove the fix")
	}
	if portOut, _ := exec.Command("docker", "port", name).Output(); len(portOut) != 0 {
		t.Fatalf("container unexpectedly has a published port (%s)", portOut)
	}

	q := HTTPPromQuerier{BaseURL: "http://" + name + ":9090"}
	holds, observed, err := q.Query("up")
	if err != nil {
		t.Fatalf("Query: %v — an IPv4-only in-stack target must still be reachable through the tunnel (try 127.0.0.1 then ::1, not just one hardcoded address)", err)
	}
	if !holds || observed != "1" {
		t.Errorf("holds=%v observed=%q, want holds=true observed=\"1\"", holds, observed)
	}
}

func waitForIPv4Server(t *testing.T, name string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		if err := exec.Command("docker", "exec", name, "wget", "-qO-", "-T", "1", "http://127.0.0.1:9090/").Run(); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("IPv4-only server never became reachable via docker exec")
}

// spec: R-CFG-17
//
// An E1 finding on the way out (not a correctness bug — nothing leaked,
// confirmed before and after): the tunnel opened a fresh container per
// dialed request. BrokerApplier's own poll loop (500ms) multiplied that
// into ~400 short-lived containers and 270s of wall clock for one corpus
// case, against 30-90s and a handful of containers for every other case.
// This proves the fix: an HTTP/1.1 keep-alive server that counts distinct
// TCP connections it accepts, queried N times in a row through the same
// (containerID-keyed, package-global — see hopTransports) cached tunnel.
// Before hopTransportFor existed, fallbackTransport built a brand new
// *http.Transport (an empty connection pool) on every RoundTrip, so
// connection reuse never had a chance regardless of the server's own
// keep-alive support; this asserts the server saw exactly one connection
// for N requests, not N.
func TestHTTPPromQuerier_ReusesTunnelAcrossRepeatedQueries(t *testing.T) {
	dockerAvailable(t)

	name := "promreuse-" + uniqueSuffix("prom")
	// Readiness is signaled via a marker file, not a probe HTTP request:
	// any real connection to port 9090 — including a readiness check —
	// would itself increment the same counter this test asserts on below,
	// so proving reuse precisely requires not touching the port at all
	// until the queries under test begin.
	script := `import http.server

class H(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    count = 0
    def do_GET(self):
        body = b'{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"1"]}]}}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def handle(self):
        H.count += 1
        with open("/tmp/conn_count", "w") as f:
            f.write(str(H.count))
        super().handle()
    def log_message(self, fmt, *args):
        pass

srv = http.server.HTTPServer(("0.0.0.0", 9090), H)
with open("/tmp/ready", "w") as f:
    f.write("1")
srv.serve_forever()
`
	out, err := exec.Command("docker", "run", "-d", "--name", name,
		"--label", "com.docker.compose.service="+name,
		"python:3-alpine", "python3", "-c", script,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	t.Cleanup(func() { closeHopTransports(); exec.Command("docker", "rm", "-f", name).Run() })
	for i := 0; ; i++ {
		if err := exec.Command("docker", "exec", name, "test", "-f", "/tmp/ready").Run(); err == nil {
			break
		}
		if i >= 50 {
			t.Fatal("server never wrote its readiness marker")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if portOut, _ := exec.Command("docker", "port", name).Output(); len(portOut) != 0 {
		t.Fatalf("container unexpectedly has a published port (%s)", portOut)
	}

	q := HTTPPromQuerier{BaseURL: "http://" + name + ":9090"}
	const n = 5
	for i := 0; i < n; i++ {
		holds, observed, err := q.Query("up")
		if err != nil {
			t.Fatalf("Query #%d: %v", i, err)
		}
		if !holds || observed != "1" {
			t.Fatalf("Query #%d: holds=%v observed=%q, want holds=true observed=\"1\"", i, holds, observed)
		}
		// BrokerApplier's own poll loop this fix targets spaces calls
		// 500ms apart; a brief gap gives Go's own connection pool
		// bookkeeping (returning the just-used connection to its idle
		// pool) time to settle between requests, the same as real usage,
		// rather than firing the next request as fast as Go can schedule
		// it — a race no real caller of this package creates.
		time.Sleep(20 * time.Millisecond)
	}

	connOut, err := exec.Command("docker", "exec", name, "cat", "/tmp/conn_count").Output()
	if err != nil {
		t.Fatalf("read connection count: %v", err)
	}
	// 2, not 1: containerHopScript's own `nc -z` liveness probe (picking
	// 127.0.0.1 vs ::1, see its doc comment) is itself one accepted TCP
	// connection, separate from the real, data-carrying one it then execs
	// into — both belong to the single tunnel this test's own dial-count
	// logging (verified separately) confirms fires exactly once for all n
	// queries. The number this test actually guards is that 2 does not
	// scale with n: the pre-fix code created one fresh tunnel (a probe +
	// a real connection each) per request, so n=5 would have shown 10.
	got := strings.TrimSpace(string(connOut))
	if got != "2" {
		t.Errorf("server accepted %s distinct TCP connections for %d queries, want 2 (one probe + one reused data connection, not one pair per query) — the tunnel should be reused via keep-alive, not re-dialed (a fresh docker run) per request", got, n)
	}
}
