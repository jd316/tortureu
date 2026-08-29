package egress_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/egress"
)

// spec: R-CFG-23
func TestValidateErrorRateRejectsAboveRange(t *testing.T) {
	err := egress.ValidateErrorRate("stripe_errors", map[string]any{"error_rate": 1.5})
	if err == nil {
		t.Fatal("ValidateErrorRate(1.5) returned nil, want an error (R-CFG-23: legal range is 0.0..1.0)")
	}
	for _, want := range []string{"stripe_errors", "error_rate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

// spec: R-CFG-23
func TestValidateErrorRateRejectsBelowRange(t *testing.T) {
	err := egress.ValidateErrorRate("stripe_errors", map[string]any{"error_rate": -0.01})
	if err == nil {
		t.Fatal("ValidateErrorRate(-0.01) returned nil, want an error (R-CFG-23: legal range is 0.0..1.0)")
	}
}

// spec: R-CFG-23
func TestValidateErrorRateRejectsWholeNumberTypo(t *testing.T) {
	// error_rate: 15 -- someone meaning "15%" written as a whole number. The
	// realistic typo R-CFG-23 exists to catch.
	err := egress.ValidateErrorRate("stripe_errors", map[string]any{"error_rate": 15})
	if err == nil {
		t.Fatal("ValidateErrorRate(15) returned nil, want an error (R-CFG-23: 15 is not a legal proportion)")
	}
}

// spec: R-CFG-23
func TestValidateErrorRateAllowsLowerBoundary(t *testing.T) {
	if err := egress.ValidateErrorRate("f1", map[string]any{"error_rate": 0.0}); err != nil {
		t.Fatalf("ValidateErrorRate(0.0): %v, want nil (0.0 is the legal lower boundary)", err)
	}
}

// spec: R-CFG-23
func TestValidateErrorRateAllowsUpperBoundary(t *testing.T) {
	if err := egress.ValidateErrorRate("f1", map[string]any{"error_rate": 1.0}); err != nil {
		t.Fatalf("ValidateErrorRate(1.0): %v, want nil (1.0 is the legal upper boundary)", err)
	}
}

// spec: R-CFG-23
func TestValidateErrorRateIgnoresFaultsWithoutErrorRate(t *testing.T) {
	if err := egress.ValidateErrorRate("f1", map[string]any{"latency": "2s"}); err != nil {
		t.Fatalf("ValidateErrorRate on a fault with no error_rate modifier: %v, want nil", err)
	}
}

// spec: R-CFG-23
func TestValidateErrorRateAcceptsTortureExampleYAML(t *testing.T) {
	// torture.example.yaml declares error_rate: 0.15, which is legal. If
	// this check rejects the project's own reference document that is a
	// Critical defect.
	raw, err := os.ReadFile(exampleYAMLPath(t))
	if err != nil {
		t.Fatalf("reading torture.example.yaml: %v", err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("config.Parse(torture.example.yaml): %v", err)
	}

	found := false
	for _, f := range cfg.Faults {
		if _, ok := f.Inject["error_rate"]; !ok {
			continue
		}
		found = true
		if err := egress.ValidateErrorRate(f.Name, f.Inject); err != nil {
			t.Errorf("ValidateErrorRate rejected torture.example.yaml's fault %q: %v", f.Name, err)
		}
	}
	if !found {
		t.Fatal("torture.example.yaml has no fault with an error_rate modifier to check against")
	}
}

// exampleYAMLPath locates torture.example.yaml at the repo root regardless
// of the test binary's working directory.
func exampleYAMLPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(dir, "torture.example.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate torture.example.yaml")
		}
		dir = parent
	}
}
