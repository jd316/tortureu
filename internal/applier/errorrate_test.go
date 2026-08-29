package applier

import (
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/config"
)

// spec: R-CFG-23
func TestTranslateErrorRate_RechecksRangeIndependentlyOfParser(t *testing.T) {
	// A Fault built directly (not via config.Parse) must still be rejected:
	// the owning layer re-checks rather than trusting the parser already
	// caught it (R-CFG-23's defence-in-depth rule, same as R-DC2-6).
	f := config.Fault{
		Name:   "stripe_errors",
		Target: "api.stripe.com",
		Verb:   "error_rate",
		Inject: map[string]any{"error_rate": 1.5},
	}
	if _, err := TranslateErrorRate(f); err == nil {
		t.Fatal("TranslateErrorRate accepted error_rate: 1.5, want an error (R-CFG-23: legal range is 0.0..1.0)")
	} else if !strings.Contains(err.Error(), "stripe_errors") || !strings.Contains(err.Error(), "error_rate") {
		t.Errorf("error %q does not name the fault and modifier", err)
	}
}

// spec: R-EXE-21
func TestTranslateErrorRate_StatusDefaultsWhenAbsent(t *testing.T) {
	// R-EXE-21: error_rate's injected status defaults to 500 when
	// unstated — a server error is what a client's retry/timeout/circuit
	// breaker paths are written against, so the default must land there,
	// not on some other status a "tidy this constant up" edit might drift
	// to (a 4xx would exercise validation handling instead, a different
	// test entirely). See TestTranslateErrorRate_UsesExplicitStatus
	// alongside this one: together they prove 500 is a *default*, not a
	// hardcoded value that happens to ignore the modifier.
	f := config.Fault{
		Name:   "stripe_errors",
		Target: "api.stripe.com",
		Verb:   "error_rate",
		Inject: map[string]any{"error_rate": 0.15},
	}
	got, err := TranslateErrorRate(f)
	if err != nil {
		t.Fatalf("TranslateErrorRate: %v", err)
	}
	if got.Status != 500 {
		t.Errorf("Status = %d, want default 500", got.Status)
	}
	if got.Rate != 0.15 {
		t.Errorf("Rate = %v, want 0.15", got.Rate)
	}
	if got.Target != "api.stripe.com" {
		t.Errorf("Target = %q, want %q", got.Target, "api.stripe.com")
	}
}

// spec: R-EXE-15
// spec: R-EXE-21
func TestTranslateErrorRate_UsesExplicitStatus(t *testing.T) {
	// The other half of R-EXE-21's proof: 500 only applies when status is
	// unstated. An explicit status (here 503, not the default) MUST be
	// honoured — otherwise TestTranslateErrorRate_StatusDefaultsWhenAbsent
	// could pass just as well with Status hardcoded to 500 and the
	// `status` modifier never read at all.
	f := config.Fault{
		Name:   "stripe_errors",
		Target: "api.stripe.com",
		Verb:   "error_rate",
		Inject: map[string]any{"error_rate": 0.15, "status": 503},
	}
	got, err := TranslateErrorRate(f)
	if err != nil {
		t.Fatalf("TranslateErrorRate: %v", err)
	}
	if got.Status != 503 {
		t.Errorf("Status = %d, want 503", got.Status)
	}
}

// spec: R-EXE-15
func TestTranslateErrorRate_RejectsWrongVerb(t *testing.T) {
	// error_rate is the only verb this package's ErrorRate translation
	// owns; a caller passing a different verb is a routing bug, not
	// something to silently coerce.
	f := config.Fault{
		Name:   "f1",
		Target: "api.stripe.com",
		Verb:   "duplicate",
		Inject: map[string]any{"duplicate": 0.1},
	}
	if _, err := TranslateErrorRate(f); err == nil {
		t.Fatal("TranslateErrorRate accepted verb \"duplicate\", want an error")
	}
}

// spec: R-CFG-23
func TestTranslateErrorRate_RejectsMissingTarget(t *testing.T) {
	f := config.Fault{
		Name:   "f1",
		Verb:   "error_rate",
		Inject: map[string]any{"error_rate": 0.15},
	}
	if _, err := TranslateErrorRate(f); err == nil {
		t.Fatal("TranslateErrorRate accepted a fault with no target, want an error")
	}
}
