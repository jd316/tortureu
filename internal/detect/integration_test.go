package detect_test

import (
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/detect"
	"github.com/jd316/TortureU/internal/egress"
)

// spec: R-DET-4
//
// This is the test whose absence hid a missing capability: every DC-2 test
// elsewhere hand-builds the map egress.Classify/CheckUnclassified consume,
// so a green suite five layers deep never proved detect.Detect could
// actually produce an unclassified host. It spans the real pipeline —
// detect.Detect -> egress.Classify -> egress.CheckUnclassified — with an
// empty torture.yaml egress policy (the "tortureu init just ran, nobody
// has classified anything yet" state), and shows a compose file with an
// external host named only in an env var produces a genuine abort, while
// the in-compose dependency does not.
func TestDetectToClassifyToCheckUnclassifiedAbortsOnExternalHostFromEnv(t *testing.T) {
	dir := t.TempDir()
	compose := writeFile(t, dir, "docker-compose.yml", `
services:
  api:
    build: .
    environment:
      STRIPE_URL: "https://api.stripe.com/v1"
  db:
    image: postgres:16
`)

	sys, err := detect.Detect(compose)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	// No torture.yaml classification has happened yet — the realistic state
	// right after `tortureu init`.
	cfg := config.Egress{Default: "deny", Hosts: map[string]config.EgressHost{}}
	classes := egress.Classify(sys.EgressClass, cfg)

	if got := classes["db:5432"]; got != egress.ClassInternal {
		t.Errorf("db:5432 classified %q, want %q — an in-compose dependency must not be swept into the abort", got, egress.ClassInternal)
	}
	if got := classes["api.stripe.com"]; got != egress.ClassUnclassified {
		t.Fatalf("api.stripe.com classified %q, want %q — detect.Detect must have surfaced it as an external host", got, egress.ClassUnclassified)
	}

	err = egress.CheckUnclassified(classes)
	if err == nil {
		t.Fatal("CheckUnclassified returned nil, want an abort: detect.Detect found an external host (api.stripe.com) that torture.yaml has not classified (R-DC2-2)")
	}
	if !strings.Contains(err.Error(), "api.stripe.com") {
		t.Errorf("abort error %q does not name api.stripe.com", err.Error())
	}
	if strings.Contains(err.Error(), "db:5432") {
		t.Errorf("abort error %q wrongly names the in-compose dependency db:5432", err.Error())
	}
}
