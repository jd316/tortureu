package main

import (
	"bytes"
	"strings"
	"testing"
)

// spec: R-CLI-1
func TestNineVerbsRecognized(t *testing.T) {
	want := []string{"init", "run", "smoke", "doctor", "mcp", "check", "emit", "capture", "replay"}
	for _, v := range want {
		var out, errb bytes.Buffer
		code := Main([]string{v}, &out, &errb)
		if code == 2 && strings.Contains(errb.String(), "unknown verb") {
			t.Errorf("verb %q not recognized: %s", v, errb.String())
		}
	}
}

// spec: R-CLI-1
func TestUnknownVerbExitsTwo(t *testing.T) {
	var out, errb bytes.Buffer
	code := Main([]string{"bogus"}, &out, &errb)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "unknown verb") {
		t.Errorf("stderr = %q, want mention of unknown verb", errb.String())
	}
}

// spec: R-CLI-1
func TestNoArgsExitsTwoWithUsage(t *testing.T) {
	var out, errb bytes.Buffer
	code := Main([]string{}, &out, &errb)
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errb.String(), "Verbs:") {
		t.Errorf("stderr = %q, want usage text", errb.String())
	}
}
