package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// spec: R-CLI-18
//
// Without --trend, run must not create the store. A verb that writes into
// the user's repo unasked is a surprise, and run is the one most likely to
// be run casually.
func TestRun_DoesNotTouchTheTrendStoreWithoutTheFlag(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, ".tortureu", "trend.jsonl")

	var out, errb bytes.Buffer
	Main([]string{"run", "-config", filepath.Join(dir, "nope.yaml")}, &out, &errb)

	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatalf("store exists without --trend (stat err = %v)", err)
	}
}

// spec: R-CLI-18
//
// --trend must be a real flag on run, not merely documented. check.py
// scans cmd/tortureu for flag registrations, so a `how:` naming it would
// otherwise pass on a flag that does not exist.
func TestRun_TrendFlagIsRegistered(t *testing.T) {
	var out, errb bytes.Buffer
	Main([]string{"run", "-h"}, &out, &errb)

	combined := out.String() + errb.String()
	if !strings.Contains(combined, "trend") {
		t.Fatalf("run's usage does not mention -trend:\n%s", combined)
	}
}
