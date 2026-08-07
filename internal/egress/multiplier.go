package egress

import "fmt"

// RealTrafficNotAllowedError is returned when a replay multiplier above 1x
// targets a class: real host without explicit opt-in. Blast radius scales
// with the multiplier, so the multiplier — not the base rate — is the knob
// that needs guarding (R-DC2-4).
type RealTrafficNotAllowedError struct {
	Multiplier float64
}

func (e *RealTrafficNotAllowedError) Error() string {
	return fmt.Sprintf("egress: replay at %vx against a real-classed host requires explicit opt-in (--allow-real-traffic)", e.Multiplier)
}

// CheckMultiplier enforces R-DC2-4's multiplier guard: replay above 1x
// against any class: real host requires allowRealTraffic to be true.
// Multipliers at or below 1x, and hosts of any other class, are unaffected.
func CheckMultiplier(classes map[string]Class, multiplier float64, allowRealTraffic bool) error {
	if multiplier <= 1 || allowRealTraffic {
		return nil
	}
	for _, class := range classes {
		if class == ClassReal {
			return &RealTrafficNotAllowedError{Multiplier: multiplier}
		}
	}
	return nil
}
