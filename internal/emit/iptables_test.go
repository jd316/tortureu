package emit

import (
	"strings"
	"testing"

	"github.com/jdb316/tortureu/internal/config"
)

// R-CLI-8 (proposed): `tortureu emit iptables` translates the `down` verb
// (connection refused) into iptables REJECT rules, run inside the SUT
// container against the fault's target. It also prints the DROP
// (blackhole) alternative as a commented-out line, since the registry note
// for this tool is explicitly that "blackhole vs RST are different client
// behaviours" and torture.yaml's schema has no way to ask for one over the
// other from the `down` verb alone.
func TestIPTables_DownOnDependency_RejectsFromSUTContainer(t *testing.T) {
	cfg := mustParse(t, netemFixture)
	out, err := IPTables(cfg)
	if err != nil {
		t.Fatalf("IPTables: %v", err)
	}
	if !strings.Contains(out, "docker exec checkout-api iptables -A OUTPUT -p tcp -d postgres --dport 5432 -j REJECT --reject-with tcp-reset") {
		t.Errorf("expected a REJECT rule against postgres from the SUT container, got:\n%s", out)
	}
	if !strings.Contains(out, "# blackhole alternative (silent drop, causes a client-side timeout instead of a refused connection):") {
		t.Errorf("expected the DROP alternative to be documented, got:\n%s", out)
	}
	if !strings.Contains(out, "# docker exec checkout-api iptables -A OUTPUT -p tcp -d postgres --dport 5432 -j DROP") {
		t.Errorf("expected the DROP alternative as a commented-out line, got:\n%s", out)
	}
}

func TestIPTables_CleansUpAfterForDuration(t *testing.T) {
	cfg := mustParse(t, netemFixture)
	out, err := IPTables(cfg)
	if err != nil {
		t.Fatalf("IPTables: %v", err)
	}
	if !strings.Contains(out, "sleep 10s && docker exec checkout-api iptables -D OUTPUT -p tcp -d postgres --dport 5432 -j REJECT --reject-with tcp-reset") {
		t.Errorf("expected a matching teardown after the fault's for: duration, got:\n%s", out)
	}
}

func TestIPTables_SkipsVerbsItDoesNotTranslate(t *testing.T) {
	cfg := mustParse(t, netemFixture)
	out, err := IPTables(cfg)
	if err != nil {
		t.Fatalf("IPTables: %v", err)
	}
	for _, name := range []string{"pg_slow", "stripe_slow", "cpu_squeeze"} {
		if !strings.Contains(out, `fault "`+name+`"`) {
			t.Errorf("expected %s to be reported as skipped, got:\n%s", name, out)
		}
	}
}

func TestIPTables_UnknownVerbErrors(t *testing.T) {
	cfg := &config.Config{
		Target: config.Target{Service: "svc"},
		Faults: []config.Fault{{Name: "bad", Target: "svc", Verb: "not_a_verb", Inject: map[string]any{"not_a_verb": true}}},
	}
	if _, err := IPTables(cfg); err == nil {
		t.Fatal("expected an error for an unrecognized verb")
	}
}
