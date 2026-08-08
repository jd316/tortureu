package smoke

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// spec: R-CLI-6
func TestNewClientReachesAnOrdinaryHostWithoutTouchingDocker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// sutService names a compose service that does not exist; if the
	// direct dial did not succeed first, this would have to fall through
	// to Docker discovery and fail — proving the fast path never shells
	// out to `docker` for an ordinarily-reachable target.
	c := NewClient("no-such-service", time.Second)
	defer c.Close()

	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// dockerAvailable skips a test rather than passing it when no Docker
// daemon is reachable — the same discipline internal/run's own
// Docker-backed tests use (docker_applier_test.go), since a
// Docker-dependent guarantee proven only "when Docker happens to be there"
// would be worse than no test.
func dockerAvailable(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}
}

// spec: R-CLI-6
func TestNewClientReachesAContainerOnAnInternalOnlyNetwork(t *testing.T) {
	dockerAvailable(t)

	const network = "tortureu-smoke-test-net"
	const service = "smoke-test-target"
	if out, err := exec.Command("docker", "network", "create", "--internal", network).CombinedOutput(); err != nil {
		t.Fatalf("docker network create: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "network", "rm", network).Run() })

	// BusyBox httpd serving a static response on 8080, attached only to
	// the internal network — no published port, mirroring what R-DC2-3's
	// topology does to the real SUT (see internal/run/load.go's package
	// doc comment).
	runArgs := []string{
		"run", "-d", "--rm",
		"--network", network,
		"--label", "com.docker.compose.service=" + service,
		"--name", "tortureu-smoke-test-target",
		"busybox:1.36", "sh", "-c",
		"mkdir -p /www && echo ok > /www/index.html && httpd -f -p 8080 -h /www",
	}
	out, err := exec.Command("docker", runArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	containerID := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", containerID).Run() })

	// Give the container a moment to start httpd.
	time.Sleep(500 * time.Millisecond)

	c := NewClient(service, 5*time.Second)
	defer c.Close()

	resp, err := c.Get("http://localhost:8080/")
	if err != nil {
		t.Fatalf("Get through container-netns tunnel: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
