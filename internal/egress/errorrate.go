package egress

import "fmt"

// ValidateErrorRate enforces R-CFG-23's legal range for the error_rate
// fault modifier: 0.0 ... 1.0. error_rate is owned by the mock provider
// (R-EXE-15 — legal only on a class: mock host), so this package must check
// it independently rather than trust that internal/config's own parse-time
// check already ran — the same defence-in-depth rationale as R-DC2-6: a
// check that lives only in another package is a convention, not a
// guarantee. inject is a fault's inject: map; a fault with no error_rate
// key is not this function's concern and passes.
func ValidateErrorRate(faultName string, inject map[string]any) error {
	v, ok := inject["error_rate"]
	if !ok {
		return nil
	}
	f, isNum := asFloat(v)
	if !isNum || f < 0.0 || f > 1.0 {
		return fmt.Errorf("egress: fault %q: inject: error_rate: %v is out of range, must be 0.0..1.0", faultName, v)
	}
	return nil
}

// asFloat extracts a numeric value decoded by yaml.v3 (int or float64).
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
