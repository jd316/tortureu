package run

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jdb316/tortureu/internal/fault"
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
	undo, err := a.ApplyToxic("redis:6379", fault.Toxic{Disable: true})
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
