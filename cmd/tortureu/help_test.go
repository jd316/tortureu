package main

import (
	"strings"
	"testing"
)

// spec: R-DC2-7
func TestHelpTextNeverClaimsDC2Guarantee(t *testing.T) {
	banned := []string{"cannot escape", "can't escape", "cannot reach the internet", "guarantee"}
	lower := strings.ToLower(usage)
	for _, phrase := range banned {
		if strings.Contains(lower, phrase) {
			t.Errorf("usage text claims the DC-2 guarantee (%q found) before it is proven end to end (R-DC2-7)", phrase)
		}
	}
}
