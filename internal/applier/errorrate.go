package applier

import (
	"fmt"

	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/egress"
)

// defaultErrorRateStatus is used when error_rate's `status` modifier is
// absent. SPEC.md's R-CFG-14 table lists status as error_rate's modifier
// without stating a default — flagged as a gap in the Task 10 report, the
// same posture as queuefault's defaultPoisonPillCount. 500 is the generic
// "the mocked dependency broke" response, the smallest thing that still
// demonstrates the failure mode error_rate exists to simulate.
const defaultErrorRateStatus = 500

// ErrorRate is one translated error_rate action against a mocked host
// (SPEC.md §4.4, R-EXE-15). Rate is a fraction of responses that MUST fail
// with Status (e.g. 0.15 for 15%), not a count or multiplier.
type ErrorRate struct {
	Target string
	Rate   float64
	Status int
}

// TranslateErrorRate converts one parsed fault into an ErrorRate action.
// error_rate is owned by the mock provider (R-EXE-15), so — unlike
// internal/fault.Translate's pass-over behaviour for verbs it does not own
// — a non-error_rate verb here is simply a caller bug and is rejected, the
// same posture internal/queuefault.Translate takes for its two verbs.
//
// It calls egress.ValidateErrorRate rather than reimplementing the range
// check: that package owns error_rate's semantics (R-EXE-15) and must
// re-verify independently of internal/config's parse-time check (R-CFG-23,
// the same defence-in-depth rule as R-DC2-6) — this function does the same
// independent re-check by calling straight into the owning validator
// instead of trusting either the parser or its own caller.
func TranslateErrorRate(f config.Fault) (ErrorRate, error) {
	if f.Verb != "error_rate" {
		return ErrorRate{}, fmt.Errorf("applier: fault %q: TranslateErrorRate called with verb %q, not \"error_rate\"", f.Name, f.Verb)
	}
	if f.Target == "" {
		return ErrorRate{}, fmt.Errorf("applier: fault %q: target is required", f.Name)
	}
	if err := egress.ValidateErrorRate(f.Name, f.Inject); err != nil {
		return ErrorRate{}, err
	}

	rate, _ := asFloat(f.Inject["error_rate"]) // ValidateErrorRate already proved this is numeric and in range
	status := defaultErrorRateStatus
	if raw, ok := f.Inject["status"]; ok {
		n, ok := asFloat(raw)
		if !ok {
			return ErrorRate{}, fmt.Errorf("applier: fault %q: status: expected a number, got %T", f.Name, raw)
		}
		status = int(n)
	}

	return ErrorRate{Target: f.Target, Rate: rate, Status: status}, nil
}

// asFloat extracts a numeric value decoded by yaml.v3 (int or float64) —
// duplicated from internal/egress/internal/config for the same reason those
// packages each keep their own copy: no shared exported helper exists, and
// this is a three-line function, not worth a new export.
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}
