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
