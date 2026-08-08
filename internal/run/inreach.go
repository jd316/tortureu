// The orchestrator's own outbound calls into a DC-2-isolated stack —
// Prometheus for promql: asserts, the broker's admin API for
// poison_pill/duplicate — hit the identical reachability problem R-DC2-3
// creates for the load generator: the target may sit exclusively on the
// SUT's internal-only network, unreachable as a plain host-process HTTP
// call (an E1 finding: "we proved the SUT could not escape, and did not
// check that we could still reach in"). K6Runner already solved this for
// the SUT itself by running k6 as a container sharing the SUT's own
// network namespace rather than dialing a published port that Docker
// never creates for an internal-only container (see load.go's package doc
// comment). This file generalizes that same mechanism — join the target's
// own netns via `docker run --network container:<id>` — for any
// http.Client this package constructs, without publishing any new port and
// without moving the target off the internal network. The DC-2 guarantee
// this package enforces elsewhere is unaffected: this reads INTO an
// already-running container's namespace from outside Docker's networking
// entirely (a subprocess's stdio pipe, not a new network attachment), so
// it adds no route OUT for the SUT and changes no container's own network
// configuration — the same reasoning that already applies to `docker
// exec`, which this is architecturally closer to than to a real dial.
package run

import (
	"context"
	"io"
	"net"
	"net/http"
	"os/exec"
	"time"
)

// containerHopImage's only requirement is a `nc` binary; alpine already
// satisfies every other real-Docker test in this package and needs no
// separate image pull.
const containerHopImage = "alpine:3.20"

// execConn adapts a subprocess's stdin/stdout pipes into a net.Conn, so an
// http.Transport's DialContext can tunnel a connection through it exactly
// as it would a real TCP dial — the same trick an SSH ProxyCommand uses.
type execConn struct {
	stdout io.ReadCloser
	stdin  io.WriteCloser
	cmd    *exec.Cmd
}

func (c *execConn) Read(b []byte) (int, error)  { return c.stdout.Read(b) }
func (c *execConn) Write(b []byte) (int, error) { return c.stdin.Write(b) }

func (c *execConn) Close() error {
	c.stdin.Close()
	c.stdout.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.cmd.Wait()
}

func (c *execConn) LocalAddr() net.Addr              { return execConnAddr{} }
func (c *execConn) RemoteAddr() net.Addr             { return execConnAddr{} }
func (c *execConn) SetDeadline(time.Time) error      { return nil }
func (c *execConn) SetReadDeadline(time.Time) error  { return nil }
func (c *execConn) SetWriteDeadline(time.Time) error { return nil }

// execConnAddr is a placeholder net.Addr: a subprocess pipe has no real
// network address, and nothing in this package's own code (or
// http.Transport's) inspects Conn.LocalAddr/RemoteAddr for anything other
// than logging.
type execConnAddr struct{}

func (execConnAddr) Network() string { return "docker-network-namespace" }
func (execConnAddr) String() string  { return "container-netns" }

// containerNetDialer returns an http.Transport-compatible DialContext that
// tunnels each connection into containerID's own network namespace:
// "localhost:<port>" inside that namespace is the container's own
// loopback, the identical address the container's own process listens on
// — the same address load.go's package doc comment documents K6Runner
// using to reach an internal-only SUT.
func containerNetDialer(containerID string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-i",
			"--network", "container:"+containerID, containerHopImage,
			"nc", "localhost", port)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return &execConn{stdout: stdout, stdin: stdin, cmd: cmd}, nil
	}
}

// fallbackTransport is an http.RoundTripper that attempts a normal, direct
// connection first and only tries reaching the request's host as a
// running compose service's own container (via discoverSUTContainer,
// already generic — keyed only by a compose service label, not
// specifically the SUT) when that direct attempt fails.
//
// This is "pick correctly rather than guess" (the coordinator's own
// framing): a genuinely external target — a user's own Prometheus,
// reachable from the host normally — always succeeds on the first
// attempt and never touches Docker at all; only an actual connection
// failure triggers the fallback, and only when the failing host names a
// container `docker ps` can actually find. A target that is neither
// reachable nor a known compose service surfaces its original, honest
// connection error — never a fabricated one.
//
// Used as HTTPPromQuerier's default transport (see client()) and,
// from run.go, explicitly wired into internal/applier.BrokerApplier's
// exported Client field — internal/applier needed no change for this:
// that field already existed for exactly this kind of substitution.
type fallbackTransport struct{}

func (fallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
	containerID, derr := discoverSUTContainer(req.URL.Hostname())
	if derr != nil {
		// Not a known compose service either: this is a genuinely
		// external or genuinely unreachable target, and the original
		// connection error is the honest thing to report — not this
		// package's own inability to find a container to blame it on.
		return nil, err
	}
	// A connection-establishment failure happens before the transport
	// ever reads the request body in the ordinary case, but restore it
	// from GetBody when available so a retried POST (BrokerApplier's own
	// calls) is not silently sent with an already-drained body.
	if req.GetBody != nil {
		if body, gerr := req.GetBody(); gerr == nil {
			req.Body = body
		}
	}
	hop := &http.Transport{DialContext: containerNetDialer(containerID)}
	return hop.RoundTrip(req)
}
