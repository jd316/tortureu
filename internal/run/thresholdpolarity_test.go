package run

import "testing"

// spec: R-VER-19
//
// The two shapes are exact opposites, measured against real containers:
//
//	                 k6 0.54.0        k6 v2.1.0
//	passed           {"ok": true}     false
//	failed           {"ok": false}    true
//
// so the shape decides the polarity. Both must normalise to "did it hold?".
func TestThresholdHeld_BothShapesNormaliseToOneMeaning(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		held  bool
	}{
		{"k6 0.54 passed", map[string]any{"ok": true}, true},
		{"k6 0.54 failed", map[string]any{"ok": false}, false},
		{"k6 v2 passed (false means not crossed)", false, true},
		{"k6 v2 failed (true means crossed)", true, false},
	} {
		got, err := thresholdHeld(tc.value)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if got != tc.held {
			t.Errorf("%s: held = %v, want %v", tc.name, got, tc.held)
		}
	}
}

// spec: R-VER-19
// spec: R-VER-18
//
// Widening to two shapes must not widen to "anything". A third shape is still
// a tool error: the danger here is a plausible format read with the wrong
// polarity, not an unparseable one.
func TestThresholdHeld_AThirdShapeIsStillRefused(t *testing.T) {
	for _, bad := range []any{"ok", 1.0, nil, []any{true}} {
		if _, err := thresholdHeld(bad); err == nil {
			t.Errorf("%T (%v) was accepted; an unrecognised shape must stay a tool error", bad, bad)
		}
	}
}
