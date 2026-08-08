package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// spec: R-CLI-10 (proposed)
func TestRunReplayRequiresFromAndTarget(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runReplay([]string{}, &out, &errb); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "-from is required") {
		t.Errorf("stderr = %q, want mention of -from", errb.String())
	}
}

// spec: R-CLI-10 (proposed)
func TestRunReplayDrivesCassetteAgainstTarget(t *testing.T) {
	var hits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	dir := t.TempDir()
	cassette := filepath.Join(dir, "cassette.jsonl")
	if err := os.WriteFile(cassette, []byte(
		`{"seq":1,"method":"GET","url":"/a","status":200}`+"\n"+
			`{"seq":2,"method":"GET","url":"/b","status":200}`+"\n",
	), 0o644); err != nil {
		t.Fatalf("write cassette fixture: %v", err)
	}

	var out, errb bytes.Buffer
	code := runReplay([]string{
		"-from", cassette,
		"-target", target.URL,
		"-host-class", "internal",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errb.String())
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("target saw %d requests, want 2", got)
	}
	if !strings.Contains(out.String(), "sent=2") {
		t.Errorf("stdout = %q, want a sent=2 summary", out.String())
	}
}

// spec: R-CLI-10 (proposed)
// spec: R-DC2-4
//
// Reuses egress.CheckMultiplier — the same guard `run` enforces for
// -multiplier/-allow-real-traffic — rather than reimplementing it: a
// multiplier above 1x against a class: real host must abort without
// -allow-real-traffic.
func TestRunReplayRejectsMultiplierAboveOneXAgainstRealHostWithoutOptIn(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	dir := t.TempDir()
	cassette := filepath.Join(dir, "cassette.jsonl")
	_ = os.WriteFile(cassette, []byte(`{"seq":1,"method":"GET","url":"/a","status":200}`+"\n"), 0o644)

	var out, errb bytes.Buffer
	code := runReplay([]string{
		"-from", cassette,
		"-target", target.URL,
		"-multiplier", "2",
	}, &out, &errb)

	if code != 2 {
		t.Fatalf("code = %d, want 2 (R-DC2-4 must reject)", code)
	}
	if !strings.Contains(errb.String(), "allow-real-traffic") {
		t.Errorf("stderr = %q, want mention of --allow-real-traffic", errb.String())
	}
}

// spec: R-CLI-10 (proposed)
func TestRunReplayAllowsMultiplierAboveOneXWithOptIn(t *testing.T) {
	var hits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	dir := t.TempDir()
	cassette := filepath.Join(dir, "cassette.jsonl")
	_ = os.WriteFile(cassette, []byte(`{"seq":1,"method":"GET","url":"/a","status":200}`+"\n"), 0o644)

	var out, errb bytes.Buffer
	code := runReplay([]string{
		"-from", cassette,
		"-target", target.URL,
		"-multiplier", "3",
		"-allow-real-traffic",
	}, &out, &errb)

	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errb.String())
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("target saw %d requests, want 3 (1 entry x 3x multiplier)", got)
	}
}
