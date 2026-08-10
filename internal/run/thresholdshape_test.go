package run

import (
	"strings"
	"testing"
)

// spec: R-VER-18
//
// k6 0.54 emits thresholds as {"expr": {"ok": true}}. k6 v2 emits a bare bool
// AND inverts it — measured against grafana/k6:latest (v2.1.0), a threshold
// that passed serialises as false, one that failed as true, because the bool
// reports "crossed" rather than "ok".
//
// The type assertion used to fail silently, leaving ok=false, so every
// assertion in the run was reported as broken: a verdict full of findings
// about a service that is fine.
func TestThresholdShape_UnknownShapeIsAToolErrorNotAFinding(t *testing.T) {
	v2 := map[string]any{
		"http_req_duration": map[string]any{
			"p(95)": 12.0,
			// k6 v2 shape: bare bool, inverted meaning.
			"thresholds": map[string]any{"p(95)<5000": false},
		},
	}
	err := validateThresholdShapes(v2)
	if err == nil {
		t.Fatal("an unrecognised threshold shape was accepted; it must be a tool error (R-VER-2)")
	}
	msg := err.Error()
	if !strings.Contains(msg, "threshold") {
		t.Errorf("error does not name the problem: %v", err)
	}
	if !strings.Contains(msg, "k6") {
		t.Errorf("error does not point at the k6 version mismatch, which is the actual cause: %v", err)
	}
}

// spec: R-VER-18
//
// The shape we do parse keeps working.
func TestThresholdShape_KnownShapeIsAccepted(t *testing.T) {
	ok54 := map[string]any{
		"http_req_duration": map[string]any{
			"p(95)":      12.0,
			"thresholds": map[string]any{"p(95)<5000": map[string]any{"ok": true}},
		},
	}
	if err := validateThresholdShapes(ok54); err != nil {
		t.Fatalf("the pinned k6's own shape was rejected: %v", err)
	}
}
