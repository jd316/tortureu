package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSmokeCompose(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(path, []byte("services:\n  app:\n    build: .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// spec: R-CLI-6
func TestSmokeRequiresURL(t *testing.T) {
	var out, errb bytes.Buffer
	code := runSmoke([]string{}, &out, &errb)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "-url") {
		t.Errorf("stderr = %q, want a mention of the missing -url flag", errb.String())
	}
}

// spec: R-CLI-6
func TestSmokePassesAgainstAHealthyServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	compose := writeSmokeCompose(t, dir)

	var out, errb bytes.Buffer
	code := runSmoke([]string{
		"-url", srv.URL,
		"-compose", compose,
		"-rate", "20",
		"-duration", "200ms",
		"-timeout", "1s",
	}, &out, &errb)

	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "sent:") || !strings.Contains(out.String(), "100.0%") {
		t.Errorf("stdout missing expected report:\n%s", out.String())
	}
}

// spec: R-CLI-6
func TestSmokeFailsAgainstADownServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // guaranteed connection-refused

	dir := t.TempDir()
	compose := writeSmokeCompose(t, dir)

	var out, errb bytes.Buffer
	code := runSmoke([]string{
		"-url", srv.URL,
		"-compose", compose,
		"-rate", "20",
		"-duration", "200ms",
		"-timeout", "200ms",
	}, &out, &errb)

	if code != 1 {
		t.Fatalf("exit = %d, want 1 (fail)", code)
	}
	if !strings.Contains(out.String(), "0.0%") {
		t.Errorf("stdout missing 0%% success rate:\n%s", out.String())
	}
}

// spec: R-CLI-6
//
// smoke must still work when there is no compose file to detect at all
// (e.g. against a plain URL with no local repo checked out) — detection
// failure is a warning, never fatal, since smoke's direct-dial fast path
// does not need it.
func TestSmokeToleratesMissingComposeFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var out, errb bytes.Buffer
	code := runSmoke([]string{
		"-url", srv.URL,
		"-compose", "/no/such/docker-compose.yml",
		"-rate", "20",
		"-duration", "100ms",
		"-timeout", "1s",
	}, &out, &errb)

	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
}

// spec: R-CLI-1
func TestSmokeVerbIsWiredIntoMain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	compose := writeSmokeCompose(t, dir)

	var out, errb bytes.Buffer
	code := Main([]string{"smoke", "-url", srv.URL, "-compose", compose, "-duration", "100ms"}, &out, &errb)
	if code != 0 {
		t.Fatalf("Main([smoke ...]) exit = %d, want 0 (stderr: %s)", code, errb.String())
	}
}
