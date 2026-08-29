package run

import (
	"errors"
	"io"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jd316/TortureU/internal/fault"
)

// TestDockerApplier_KillDoesNotProduceClientVisibleRST is committed evidence
// for a B1 finding: "kill" (docker kill --signal SIGKILL, verified genuinely
// distinct from "graceful"/SIGTERM by
// TestDockerApplier_KillAndGracefulSendGenuinelyDistinctSignals's exit-code
// check) still measured as a graceful close, not a TCP RST, at the client.
// A test asserting the signal was *sent* is not evidence the connection was
// *reset* — this test reads from the client's own socket after the kill,
// the only place that distinction is observable, across two topologies:
//
//  1. published port (host -> container, this test)
//  2. shared network namespace, the same path K6Runner's container mode
//     uses (verified manually during investigation, same result: recv()
//     returns 0 bytes / io.EOF, never syscall.ECONNRESET, in both cases)
//
// Conclusion: SIGKILL to a container's PID 1 does not produce a
// client-visible RST in this environment. This is standard Linux socket
// teardown, not a Docker-layer or this-applier defect: when a process's
// file descriptors are released (by exit or by the kernel after a fatal
// signal), an idle, fully-drained TCP socket is torn down with an orderly
// FIN, not an abortive RST — RST requires either unread data still queued
// at the moment of close, an explicit SO_LINGER{on,0} abortive close set by
// the application itself, or an intermediary actively injecting a reset
// (which is exactly what internal/fault's Toxiproxy-side "reset_peer" toxic
// already exists to do — a network-layer fault, not a process-layer one).
// DockerApplier has no access to the target process's socket options and
// cannot make it call an abortive close on its way down.
//
// This is reported as a legitimate spec-level distinction rather than
// silently claimed as fixed: "kill" (process death) and a genuine
// client-visible RST are two different failure classes, and SPEC's kill
// verb, implemented at the only layer this applier can reach (signaling
// the container's process), produces the former.
//
// This finding is what R-EXE-25 now codifies (pause/kill/graceful are
// distinct at the signal/exit-code layer, not the client-visible TCP
// layer); this test is R-EXE-25's verification. It is not R-CFG-15
// evidence — R-CFG-15 is proven by
// TestDockerApplier_KillAndGracefulSendGenuinelyDistinctSignals (the
// signal/exit-code distinction itself); this test proves the separate,
// narrower claim that the distinction does not extend to a client-visible
// RST.
//
// spec: R-EXE-25
func TestDockerApplier_KillDoesNotProduceClientVisibleRST(t *testing.T) {
	dockerAvailable(t)

	out, err := exec.Command("docker", "run", "-d", "-P", "nginx:alpine").Output()
	if err != nil {
		t.Fatalf("docker run: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })

	portOut, err := exec.Command("docker", "port", id, "80/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	addr := strings.TrimSpace(strings.Split(strings.TrimSpace(string(portOut)), "\n")[0])
	if addr == "" {
		t.Fatal("docker port returned no mapping for 80/tcp")
	}
	addr = strings.Replace(addr, "0.0.0.0:", "127.0.0.1:", 1)

	var conn net.Conn
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		conn, err = net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("could not connect to %s: %v", addr, err)
	}
	defer conn.Close()

	// No request sent: nginx just waits for one on an established,
	// otherwise-idle connection, which is exactly the state under test —
	// an idle client connection at the moment its backend process dies.
	// (Sending a request and reading the response first would leave HTTP
	// keep-alive semantics — how much of the response was actually
	// buffered vs. delivered — entangled with the kill's effect on the
	// close; idle-and-unread avoids that ambiguity entirely.)

	a := DockerApplier{}
	if _, err := a.ApplyDocker("f1", fault.DockerAction{
		Kind: "kill", Container: id, Args: map[string]any{"signal": "SIGKILL"},
	}); err != nil {
		t.Fatalf("ApplyDocker(kill): %v", err)
	}

	buf := make([]byte, 65536)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, readErr := conn.Read(buf)

	var errno syscall.Errno
	if errors.As(readErr, &errno) && errno == syscall.ECONNRESET {
		t.Fatalf("got ECONNRESET — if this environment/image combination now produces a genuine client-visible RST from docker kill, update this test's doc comment (and the B1 report) to reflect it; do not just delete the assertion")
	}
	if readErr != io.EOF {
		t.Fatalf("read after kill = %v, want io.EOF (this test documents the current graceful-close behavior; if the error shape changed, investigate before updating the expectation)", readErr)
	}
}
