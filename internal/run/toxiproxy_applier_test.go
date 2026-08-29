package run

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jd316/TortureU/internal/fault"
)

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// spec: R-EXE-15
func TestToxiproxyApplier_CreatesProxyThenAddsToxicThenUndoRemovesIt(t *testing.T) {
	var created, added, removed bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/proxies":
			created = true
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/proxies/postgres:5432/toxics":
			added = true
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete && r.URL.Path == "/proxies/postgres:5432/toxics/pg_slow_latency":
			removed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := &ToxiproxyApplier{BaseURL: srv.URL}
	a.RegisterTarget("pg_slow", "postgres:5432")
	undo, err := a.ApplyToxic("pg_slow", fault.Toxic{Type: "latency", Attributes: map[string]any{"latency": "300ms"}})
	if err != nil {
		t.Fatalf("ApplyToxic: %v", err)
	}
	if !created || !added {
		t.Fatalf("created=%v added=%v, want both true", created, added)
	}
	if err := undo(); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if !removed {
		t.Error("toxic was never removed by undo")
	}
}

// spec: R-DC2-3
func TestResolveUpstream_LeavesIPLiteralUnchanged(t *testing.T) {
	// The proxy container is itself a member of the SUT-side network that
	// aliases a classified hostname to it (topology.go); resolving that
	// hostname the same way Docker's embedded DNS would (from inside that
	// network) would return the proxy's own address — a self-loop. An IP
	// literal has nothing to resolve, so it passes through untouched.
	if got := resolveUpstream("10.0.0.5:443"); got != "10.0.0.5:443" {
		t.Errorf("resolveUpstream(IP literal) = %q, want unchanged", got)
	}
}

// spec: R-DC2-3
func TestResolveUpstream_ResolvesHostnameToRealAddress(t *testing.T) {
	// localhost always resolves via this process's own ordinary DNS/hosts
	// file — never through any Docker network alias — proving resolution
	// happens here, not inside the (potentially self-aliased) target.
	got := resolveUpstream("localhost:80")
	if got == "localhost:80" {
		t.Error("resolveUpstream(localhost:80) left the hostname unresolved")
	}
	if _, _, err := net.SplitHostPort(got); err != nil {
		t.Errorf("resolveUpstream returned %q, not a valid host:port: %v", got, err)
	}
}

// spec: R-EXE-15
func TestToxiproxyApplier_ApplyToxicErrorsWhenTargetNeverRegistered(t *testing.T) {
	// Silently falling back to the fault name as the target would proxy the
	// wrong host without telling anyone (review finding: "silent
	// mistargeting"). An unregistered fault must fail loudly instead.
	a := &ToxiproxyApplier{BaseURL: "http://unused.invalid"}
	if _, err := a.ApplyToxic("never_registered", fault.Toxic{Type: "latency"}); err == nil {
		t.Fatal("ApplyToxic returned nil error for an unregistered fault name, want an error naming the fault")
	}
}

// spec: R-CFG-14
func TestToxiproxyApplier_DownVerbDisablesProxyAndUndoReenables(t *testing.T) {
	var disabled, enabled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/proxies":
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && r.URL.Path == "/proxies/redis:6379":
			var body struct {
				Enabled bool `json:"enabled"`
			}
			_ = readJSON(r, &body)
			if body.Enabled {
				enabled = true
			} else {
				disabled = true
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := &ToxiproxyApplier{BaseURL: srv.URL}
	a.RegisterTarget("redis_dies", "redis:6379")
	undo, err := a.ApplyToxic("redis_dies", fault.Toxic{Disable: true})
	if err != nil {
		t.Fatalf("ApplyToxic: %v", err)
	}
	if !disabled {
		t.Fatal("proxy was never disabled")
	}
	if err := undo(); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if !enabled {
		t.Error("undo never re-enabled the proxy")
	}
}
