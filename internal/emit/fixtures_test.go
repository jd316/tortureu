package emit

import (
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/config"
	"github.com/jd316/tortureu/internal/detect"
)

func fixturesCfg(seed string) *config.Config {
	return &config.Config{
		Target: config.Target{Service: "checkout-api"},
		Reset:  config.Reset{Seed: seed},
	}
}

// R-CLI-8 (proposed): one `tortureu emit fixtures` verb picks the library
// from the detected language — gofakeit for Go.
func TestFixtures_GoUsesGofakeit(t *testing.T) {
	// spec: R-CLI-8
	out, err := attackFixtures(fixturesCfg("orders-42"), &detect.System{Lang: "go"})
	if err != nil {
		t.Fatalf("attackFixtures: %v", err)
	}
	if !strings.Contains(out, "github.com/brianvoe/gofakeit/v7") {
		t.Errorf("expected gofakeit for a Go project, got:\n%s", out)
	}
	if strings.Contains(out, "from faker import") || strings.Contains(out, "@faker-js/faker") {
		t.Errorf("Go output must not reference the Faker libraries, got:\n%s", out)
	}
}

// R-CLI-8 (proposed): Faker (Python) for a Python project.
func TestFixtures_PythonUsesFaker(t *testing.T) {
	// spec: R-CLI-8
	out, err := attackFixtures(fixturesCfg("orders-42"), &detect.System{Lang: "python"})
	if err != nil {
		t.Fatalf("attackFixtures: %v", err)
	}
	if !strings.Contains(out, "from faker import Faker") {
		t.Errorf("expected the Python Faker, got:\n%s", out)
	}
}

// R-CLI-8 (proposed): @faker-js/faker for a JS/Node project (detected lang
// "node").
func TestFixtures_JSUsesFaker(t *testing.T) {
	// spec: R-CLI-8
	out, err := attackFixtures(fixturesCfg("orders-42"), &detect.System{Lang: "node"})
	if err != nil {
		t.Fatalf("attackFixtures: %v", err)
	}
	if !strings.Contains(out, "@faker-js/faker") {
		t.Errorf("expected the JS Faker, got:\n%s", out)
	}
}

// R-CLI-8 / R-COV-6: with no detected language the library is unknown and
// MUST NOT be defaulted — the tool says so and emits no library code.
func TestFixtures_UndetectedLanguageRefuses(t *testing.T) {
	// spec: R-CLI-8
	out, err := attackFixtures(fixturesCfg("orders-42"), &detect.System{Lang: ""})
	if err != nil {
		t.Fatalf("attackFixtures: %v", err)
	}
	if strings.Contains(out, "github.com/brianvoe/gofakeit") ||
		strings.Contains(out, "from faker import") ||
		strings.Contains(out, "@faker-js/faker") {
		t.Errorf("expected no library code when the language is undetected, got:\n%s", out)
	}
	low := strings.ToLower(out)
	if !strings.Contains(low, "not detected") && !strings.Contains(low, "could not") {
		t.Errorf("expected an explicit not-detected refusal, got:\n%s", out)
	}
}

// R-CLI-8 / D-8: the generator is seeded deterministically from
// torture.yaml's reset.seed so fixtures are reproducible across runs.
func TestFixtures_SeedsDeterministicallyFromResetSeed(t *testing.T) {
	// spec: R-CLI-8
	a, err := attackFixtures(fixturesCfg("orders-42"), &detect.System{Lang: "go"})
	if err != nil {
		t.Fatalf("attackFixtures: %v", err)
	}
	b, err := attackFixtures(fixturesCfg("orders-42"), &detect.System{Lang: "go"})
	if err != nil {
		t.Fatalf("attackFixtures: %v", err)
	}
	if a != b {
		t.Error("same reset.seed must produce identical output")
	}
	c, err := attackFixtures(fixturesCfg("different-seed"), &detect.System{Lang: "go"})
	if err != nil {
		t.Fatalf("attackFixtures: %v", err)
	}
	if a == c {
		t.Error("a different reset.seed must change the embedded seed")
	}
	if !strings.Contains(a, "reset.seed") {
		t.Errorf("expected the header to attribute the seed to reset.seed, got:\n%s", a)
	}
}

// R-CLI-8: the fixtures verb is registered and needs *detect.System (it
// picks the library from the detected language).
func TestFixtures_Registered(t *testing.T) {
	// spec: R-CLI-8
	if _, ok := registry["fixtures"]; !ok {
		t.Error("fixtures emitter was not registered")
	}
	if !NeedsSystem("fixtures") {
		t.Error("fixtures must need *detect.System to read the detected language")
	}
}
