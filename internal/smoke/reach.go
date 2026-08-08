// Reachability for `smoke`'s HTTP client.
//
// internal/run/inreach.go already solved this exact problem for `run`'s own
// outbound calls into a DC-2-isolated stack (Prometheus, a broker's admin
// API), and internal/run/load.go's K6Runner solves it for the load
// generator itself: an internal-only Docker network publishes no host port
// (Docker does not publish ports for a container whose only network is
// internal), so a plain host-process HTTP client dialing the SUT's normal
// address gets connection-refused against an isolated stack. A smoke check
// that only worked against a non-isolated stack would repeat that exact
// bug — the task brief calls it "the largest bug this project has had".
//
// The mechanism here is the same one inreach.go's fallbackTransport and
// containerNetDialer use: try a normal direct connection first, and only on
// failure join the target container's own network namespace (`docker run
// --network container:<id>`, the same trick `docker exec` uses) and tunnel
// through a subprocess's stdio pipes. It is reimplemented here rather than
// imported because inreach.go's fallbackTransport, containerNetDialer, and
// discoverSUTContainer are all unexported in internal/run, and this task's
// brief does not permit editing internal/run to export them.
package smoke

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Client wraps an *http.Client whose transport tries a direct connection
// first and falls back to a container-network-namespace tunnel. Close must
// be called once the client is done being used, to kill any tunnel
// subprocesses it started (see tunnelTransport.closeAll).
type Client struct {
	*http.Client
	transport *tunnelTransport
}

// NewClient builds a Client that reaches url the normal way when possible,
// and otherwise tunnels into the running container for the given compose
// service name (sutService — typically the detected SUT, detect.System.SUT)
// via its Docker network namespace. timeout bounds each request.
func NewClient(sutService string, timeout time.Duration) *Client {
	t := &tunnelTransport{sutService: sutService, hops: map[string]*http.Transport{}}
	return &Client{
		Client:    &http.Client{Timeout: timeout, Transport: t},
		transport: t,
	}
}

// Close kills every tunnel subprocess this client opened. Safe to call
// whether or not any tunnel was ever needed.
func (c *Client) Close() {
	c.transport.closeAll()
}

// containerHopImage/containerHopScript/execConn/containerNetDialer mirror
// internal/run/inreach.go's mechanism of the same names; see that file's
// doc comments for the full reasoning (the v4-then-v6 loopback probe, why a
// bare EOF is turned into a descriptive error, why a subprocess pipe is
// adapted into a net.Conn at all).
const containerHopImage = "alpine:3.20"

const containerHopScript = `port="$1"
for ip in 127.0.0.1 ::1; do
  if nc -z -w1 "$ip" "$port" 2>/dev/null; then
    exec nc "$ip" "$port"
  fi
done
echo "no loopback address (127.0.0.1, ::1) is accepting connections on port $port inside this container" >&2
exit 1
`

type execConn struct {
	stdout io.ReadCloser
	stdin  io.WriteCloser
	cmd    *exec.Cmd
	stderr *bytes.Buffer

	containerID, port string

	waitOnce sync.Once
	waitErr  error
}

func (c *execConn) wait() error {
	c.waitOnce.Do(func() { c.waitErr = c.cmd.Wait() })
	return c.waitErr
}

func (c *execConn) Read(b []byte) (int, error) {
	n, err := c.stdout.Read(b)
	if err == nil || n > 0 {
		return n, err
	}
	done := make(chan struct{})
	go func() { c.wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		return n, err // still running: an ordinary, live-connection EOF
	}
	if c.waitErr == nil {
		return n, err // exited cleanly: an ordinary closed connection
	}
	stderrText := strings.TrimSpace(c.stderr.String())
	msg := fmt.Sprintf("smoke: container network tunnel into %s port %s failed (%v)", c.containerID, c.port, c.waitErr)
	if stderrText != "" {
		msg += ": " + stderrText
	}
	return n, fmt.Errorf("%s", msg)
}

func (c *execConn) Write(b []byte) (int, error) { return c.stdin.Write(b) }

func (c *execConn) Close() error {
	c.stdin.Close()
	c.stdout.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.wait()
}

func (c *execConn) LocalAddr() net.Addr              { return execConnAddr{} }
func (c *execConn) RemoteAddr() net.Addr             { return execConnAddr{} }
func (c *execConn) SetDeadline(time.Time) error      { return nil }
func (c *execConn) SetReadDeadline(time.Time) error  { return nil }
func (c *execConn) SetWriteDeadline(time.Time) error { return nil }

type execConnAddr struct{}

func (execConnAddr) Network() string { return "docker-network-namespace" }
func (execConnAddr) String() string  { return "container-netns" }

func containerNetDialer(containerID string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-i",
			"--network", "container:"+containerID, containerHopImage,
			"sh", "-c", containerHopScript, "sh", port)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return &execConn{
			stdout: stdout, stdin: stdin, cmd: cmd, stderr: &stderr,
			containerID: containerID, port: port,
		}, nil
	}
}

// tunnelTransport is smoke's equivalent of inreach.go's fallbackTransport:
// try a normal direct connection first, and only fall back to a
// container-netns tunnel — discovered by sutService's compose label, the
// same lookup discoverSUTContainer performs — when the direct attempt
// fails. A target that is neither reachable nor a known compose service
// surfaces its original, honest connection error.
//
// hops caches one *http.Transport per discovered containerID for the
// lifetime of this tunnelTransport (see hopTransports' doc comment in
// inreach.go for why this matters: a fresh *http.Transport, and so a fresh
// `docker run` subprocess, per request would make a whole smoke run cost
// one throwaway container per request instead of one persistent tunnel
// reused via HTTP keep-alive).
type tunnelTransport struct {
	sutService string

	mu   sync.Mutex
	hops map[string]*http.Transport
}

func (t *tunnelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
	if t.sutService == "" {
		return nil, err
	}
	containerID, derr := discoverContainerForService(t.sutService)
	if derr != nil {
		return nil, err
	}
	retry := req.Clone(req.Context())
	if req.GetBody != nil {
		if body, gerr := req.GetBody(); gerr == nil {
			retry.Body = body
		}
	}
	return t.hopTransportFor(containerID).RoundTrip(retry)
}

func (t *tunnelTransport) hopTransportFor(containerID string) *http.Transport {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ht, ok := t.hops[containerID]; ok {
		return ht
	}
	ht := &http.Transport{
		DialContext:     containerNetDialer(containerID),
		IdleConnTimeout: 30 * time.Second,
	}
	t.hops[containerID] = ht
	return ht
}

func (t *tunnelTransport) closeAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, ht := range t.hops {
		ht.CloseIdleConnections()
		delete(t.hops, id)
	}
}

// discoverContainerForService finds the running Docker container for a
// compose service, the same lookup internal/run's discoverSUTContainer
// performs (docker ps filtered by the label compose itself applies to
// every container it creates).
func discoverContainerForService(service string) (string, error) {
	out, err := exec.Command("docker", "ps", "--filter", "label=com.docker.compose.service="+service, "--format", "{{.Names}}").Output()
	if err != nil {
		return "", fmt.Errorf("docker ps: %w", err)
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return "", fmt.Errorf("no running container found for compose service %q", service)
	}
	return names[0], nil
}
