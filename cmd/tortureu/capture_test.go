package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// freePort asks the OS for a free TCP port by binding to :0 and releasing
// it immediately. There is a small window where another process could grab
// the same port before runCapture rebinds it, but that race is standard
// practice for this kind of test and not worth a synchronization mechanism.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

const captureCLIKnownSecret = "sk_live_51H8x9secretDONOTLEAK"

// spec: R-CLI-9 (proposed)
func TestRunCaptureRequiresUpstream(t *testing.T) {
	var out, errb bytes.Buffer
	code := runCapture([]string{}, &out, &errb)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "-upstream is required") {
		t.Errorf("stderr = %q, want mention of -upstream", errb.String())
	}
}

// spec: R-CLI-9 (proposed)
// spec: R-DC2-5
//
// End-to-end through the actual CLI verb: a real credential sent through
// `tortureu capture` must never appear in the cassette file the verb
// writes, proven by reading the file back from disk.
func TestRunCaptureWritesScrubbedCassetteToDisk(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cassette := filepath.Join(dir, "cassette.jsonl")
	addr := freePort(t)

	var out, errb bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runCapture([]string{
			"-upstream", upstream.URL,
			"-listen", addr,
			"-out", cassette,
			"-duration", "300ms",
		}, &out, &errb)
	}()

	// Give the server a moment to bind before sending through it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("tcp", addr); err == nil {
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/login",
		strings.NewReader(`{"password":"`+captureCLIKnownSecret+`"}`))
	req.Header.Set("Authorization", "Bearer "+captureCLIKnownSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request to capture proxy: %v", err)
	}
	resp.Body.Close()

	code := <-done
	if code != 0 {
		t.Fatalf("runCapture exit = %d, stderr = %s", code, errb.String())
	}

	onDisk, err := os.ReadFile(cassette)
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	if strings.Contains(string(onDisk), captureCLIKnownSecret) {
		t.Fatalf("R-DC2-5 VIOLATED via CLI: credential found on disk: %s", onDisk)
	}
}

// spec: R-CLI-12 (proposed)
//
// An unrecognised engine must error listing the engines that exist. A
// silent fallback to the proxy would leave the user believing keploy ran.
func TestRunCaptureRejectsUnknownEngine(t *testing.T) {
	var out, errb bytes.Buffer
	code := runCapture([]string{"-upstream", "http://127.0.0.1:1", "-engine", "wireshark"}, &out, &errb)
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	msg := errb.String()
	for _, want := range []string{"wireshark", "proxy", "keploy"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr = %q, want it to mention %q", msg, want)
		}
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing captured for an unknown engine", out.String())
	}
}

// spec: R-CLI-12 (proposed)
//
// -engine keploy is a delegate handoff: it prints keploy's own command and
// config for the detected system and exits without capturing anything
// itself. -upstream is the proxy engine's input and must not be required.
func TestRunCaptureKeployEngineHandsOff(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(compose, []byte(
		"services:\n  api:\n    build: .\n    container_name: myapp-api\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	var out, errb bytes.Buffer
	code := runCapture([]string{"-engine", "keploy", "-compose", compose}, &out, &errb)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{"keploy record", "--container-name myapp-api", "keploy.yml"} {
		if !strings.Contains(got, want) {
			t.Errorf("stdout missing %q:\n%s", want, got)
		}
	}
	// It must be honest about not having run anything.
	if !strings.Contains(got, "delegate") {
		t.Errorf("stdout must state the delegate handoff:\n%s", got)
	}
}

// spec: R-CLI-12 (proposed)
//
// A compose file with no container_name: for the SUT is refused, naming the
// missing input — never a guessed container name.
func TestRunCaptureKeployEngineRefusesGuess(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(compose, []byte("services:\n  api:\n    build: .\n"), 0o644); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	var out, errb bytes.Buffer
	code := runCapture([]string{"-engine", "keploy", "-compose", compose}, &out, &errb)
	if code != 2 {
		t.Fatalf("code = %d, want 2 (stdout=%s)", code, out.String())
	}
	if !strings.Contains(errb.String(), "container_name") {
		t.Errorf("stderr = %q, want it to name container_name", errb.String())
	}
}
