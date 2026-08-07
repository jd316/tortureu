// ToxiproxyApplier is the real implementation of fault.Applier's ApplyToxic
// for network-scoped faults (R-EXE-15's Toxiproxy row), talking to a running
// Toxiproxy instance's HTTP control API (RESEARCH.md §"Toxiproxy": "HTTP API
// = scriptable on a timeline"). fault.Manager's Applier seam had only a fake
// implementation before this task; this is the real one.
//
// A proxy for target MUST already exist in Toxiproxy before a toxic can be
// added to it — Toxiproxy's own API has no "create proxy and toxic in one
// call". This package creates the proxy on first use if it is missing
// (idempotent: a 409 from an existing proxy is not an error here).
//
// fault.Applier.ApplyToxic(name string, t Toxic) receives only the fault's
// Name and its Toxic (type/disable/attributes) — fault.Toxic carries no
// upstream host:port, so nothing in that call tells this package what to
// proxy (escalated in the Task 7 report: fault.Toxic has no Target field).
// The workaround, since internal/fault is read-only for this task:
// scheduler.go calls RegisterTarget(faultName, target) — using
// config.Fault.Target, which it already has — immediately before routing
// the action to fault.Manager.Apply, so ApplyToxic can look the real
// upstream back up by fault name.
package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/jdb316/tortureu/internal/fault"
)

// ToxiproxyApplier drives a Toxiproxy instance's HTTP control API.
type ToxiproxyApplier struct {
	// BaseURL is Toxiproxy's control API, e.g. "http://localhost:8474".
	BaseURL string
	// Client defaults to http.DefaultClient.
	Client *http.Client

	mu      sync.Mutex
	targets map[string]string // fault name -> upstream host:port
}

// RegisterTarget records the upstream host:port a fault (by name) should be
// proxied to. See the package doc comment for why this exists.
func (a *ToxiproxyApplier) RegisterTarget(faultName, target string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.targets == nil {
		a.targets = map[string]string{}
	}
	a.targets[faultName] = target
}

func (a *ToxiproxyApplier) targetFor(faultName string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.targets[faultName]; ok {
		return t
	}
	return faultName // unregistered: best effort, matches earlier behavior
}

func (a *ToxiproxyApplier) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return http.DefaultClient
}

func (a *ToxiproxyApplier) do(method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.BaseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, err
}

// ensureProxy creates a Toxiproxy proxy named after target if one does not
// already exist, so ApplyToxic has somewhere to attach a toxic.
func (a *ToxiproxyApplier) ensureProxy(target string) error {
	body, status, err := a.do(http.MethodPost, "/proxies", map[string]any{
		"name":     target,
		"listen":   "0.0.0.0:0",
		"upstream": target,
	})
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusCreated || status == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("run: toxiproxy: create proxy %s: status %d: %s", target, status, body)
}

// ApplyToxic implements fault.Applier: it ensures a proxy exists for the
// fault's target, then either disables the proxy (t.Disable, R-CFG-14's
// down: verb — connection refused) or adds t as a named toxic, returning an
// undo that reverses whichever it did.
func (a *ToxiproxyApplier) ApplyToxic(name string, t fault.Toxic) (func() error, error) {
	target := a.targetFor(name)
	if err := a.ensureProxy(target); err != nil {
		return nil, err
	}

	if t.Disable {
		body, status, err := a.do(http.MethodPost, "/proxies/"+target, map[string]any{"enabled": false})
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("run: toxiproxy: disable %s: status %d: %s", target, status, body)
		}
		return func() error {
			_, status, err := a.do(http.MethodPost, "/proxies/"+target, map[string]any{"enabled": true})
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return fmt.Errorf("run: toxiproxy: re-enable %s: status %d", target, status)
			}
			return nil
		}, nil
	}

	toxicName := name + "_" + t.Type
	body, status, err := a.do(http.MethodPost, "/proxies/"+target+"/toxics", map[string]any{
		"name":       toxicName,
		"type":       t.Type,
		"attributes": t.Attributes,
	})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return nil, fmt.Errorf("run: toxiproxy: add toxic %s on %s: status %d: %s", t.Type, target, status, body)
	}
	return func() error {
		_, status, err := a.do(http.MethodDelete, "/proxies/"+target+"/toxics/"+toxicName, nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK && status != http.StatusNoContent {
			return fmt.Errorf("run: toxiproxy: remove toxic %s on %s: status %d", toxicName, target, status)
		}
		return nil
	}, nil
}

// ApplyDocker is not implemented by ToxiproxyApplier: container/cgroup
// faults are Docker's (R-EXE-15's Docker/cgroup row). CombinedApplier
// always routes a KindDocker action to DockerApplier instead.
func (a *ToxiproxyApplier) ApplyDocker(name string, d fault.DockerAction) (func() error, error) {
	return nil, fmt.Errorf("run: ToxiproxyApplier does not apply container actions (fault %q)", name)
}
