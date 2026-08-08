package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const emitFixtureYAML = `
version: 0
target:
  compose: ./docker-compose.yml
  service: checkout-api
  base_url: http://localhost:8080
egress:
  default: deny
  hosts:
    postgres:5432: { class: internal }
load:
  engine: k6
  model: arrival_rate
  stages:
    - phase: peak
      hold: 500rps
      for: 60s
faults:
  - name: pg_slow
    at: peak
    for: 30s
    target: postgres:5432
    inject: { latency: 300ms, jitter: 50ms }
assert:
  - http_req_duration: ["p(95)<500"]
`

func writeEmitFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "torture.yaml")
	if err := os.WriteFile(path, []byte(emitFixtureYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// spec: R-CLI-8 (proposed)
func TestEmit_MissingToolArgExitsTwoWithUsage(t *testing.T) {
	path := writeEmitFixture(t)
	var out, errb bytes.Buffer
	code := Main([]string{"emit", "-config", path}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "usage: tortureu emit") {
		t.Errorf("stderr = %q, want usage text", errb.String())
	}
}

// spec: R-CLI-8 (proposed)
func TestEmit_UnknownToolExitsTwoListingSupported(t *testing.T) {
	path := writeEmitFixture(t)
	var out, errb bytes.Buffer
	code := Main([]string{"emit", "-config", path, "gatling"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "pumba") {
		t.Errorf("stderr = %q, want the list of supported tools", errb.String())
	}
}

// spec: R-CLI-8 (proposed)
//
// Output goes to stdout, not a file (TBD-2 resolved): this is what lets
// `tortureu emit pumba > chaos.sh` compose the way the task brief asks.
func TestEmit_PumbaPrintsToStdout(t *testing.T) {
	path := writeEmitFixture(t)
	var out, errb bytes.Buffer
	code := Main([]string{"emit", "-config", path, "pumba"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, errb.String())
	}
	if errb.String() != "" {
		t.Errorf("stderr = %q, want empty on success", errb.String())
	}
	if !strings.Contains(out.String(), "pumba netem") {
		t.Errorf("stdout = %q, want a pumba netem command", out.String())
	}
}

// spec: R-CLI-8 (proposed)
func TestEmit_MissingConfigExitsTwo(t *testing.T) {
	var out, errb bytes.Buffer
	code := Main([]string{"emit", "-config", "/nonexistent/torture.yaml", "pumba"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "torture.yaml") {
		t.Errorf("stderr = %q, want mention of the missing config", errb.String())
	}
}

// spec: R-CLI-8 (proposed)
func TestEmit_NetemAndIPTablesAlsoWireUp(t *testing.T) {
	path := writeEmitFixture(t)
	for _, tool := range []string{"netem", "iptables"} {
		var out, errb bytes.Buffer
		code := Main([]string{"emit", "-config", path, tool}, &out, &errb)
		if code != 0 {
			t.Errorf("%s: exit = %d, want 0; stderr: %s", tool, code, errb.String())
		}
		if out.Len() == 0 {
			t.Errorf("%s: expected non-empty stdout", tool)
		}
	}
}
