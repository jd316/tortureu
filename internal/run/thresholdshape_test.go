package run

import (
	"strings"
	"testing"
)

// spec: R-VER-18
//
// R-VER-19 taught the parser k6 v2's bare bool, so that shape is now accepted
// and normalised rather than refused. What survives is the guard that made
// widening safe: a shape that is NEITHER known form is still a tool error.
//
// The danger this protects against is not an unparseable summary — it is a
// plausible one read with inverted meaning. k6 v2's bool means "crossed",
// the exact opposite of 0.54's "ok", so accepting a third shape on the
// assumption it means the same thing would reintroduce the bug.
func TestThresholdShape_UnknownShapeIsAToolErrorNotAFinding(t *testing.T) {
	unknown := map[string]any{
		"http_req_duration": map[string]any{
			"p(95)":      12.0,
			"thresholds": map[string]any{"p(95)<5000": "breached"},
		},
	}
	err := validateThresholdShapes(unknown)
	if err == nil {
		t.Fatal("an unrecognised threshold shape was accepted; it must be a tool error (R-VER-2)")
	}
	if !strings.Contains(err.Error(), "threshold") {
		t.Errorf("error does not name the problem: %v", err)
	}
}

// spec: R-VER-19
//
// And the k6 v2 shape, which used to be refused, is now read correctly.
func TestThresholdShape_K6V2BareBoolIsAccepted(t *testing.T) {
	v2 := map[string]any{
		"http_req_duration": map[string]any{
			"p(95)":      12.0,
			"thresholds": map[string]any{"p(95)<5000": false},
		},
	}
	if err := validateThresholdShapes(v2); err != nil {
		t.Fatalf("k6 v2's shape was rejected after R-VER-19: %v", err)
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
