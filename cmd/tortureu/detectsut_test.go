package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jd316/TortureU/internal/detect"
)

// spec: R-DET-19
//
// R-DET-19 refuses to name a SUT when several build: services are candidates,
// which is honest — but `run` and `emit` already hold an authoritative answer
// in target.service, and detection returning "" there would silently degrade
// two things that key off sys.SUT: the audit-candidate join (R-VER-4, the
// net/http Client.Timeout path) and the Jaeger service lookup (R-VER-13).
// Both fail closed rather than loudly, so this must be pinned.
func TestDetection_HonoursTargetServiceOnAnAmbiguousStack(t *testing.T) {
	dir := t.TempDir()
	compose := filepath.Join(dir, "compose.yaml")
	// Two build: services, neither depended on by the other — genuinely
	// ambiguous, so a bare Detect names no SUT.
	const yml = `services:
  vote:
    build: ./vote
  worker:
    build: ./worker
`
	if err := os.WriteFile(compose, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}

	bare, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if bare.SUT != "" {
		t.Fatalf("precondition: bare Detect named %q; this stack should be ambiguous", bare.SUT)
	}

	withSUT, err := detect.DetectWithSUT(compose, "vote")
	if err != nil {
		t.Fatalf("DetectWithSUT: %v", err)
	}
	if withSUT.SUT != "vote" {
		t.Fatalf("SUT = %q, want the authoritative target.service %q", withSUT.SUT, "vote")
	}
}
