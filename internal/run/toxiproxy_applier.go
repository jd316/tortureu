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
	"net"
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

func (a *ToxiproxyApplier) targetFor(faultName string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.targets[faultName]
	return t, ok
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

// ensureProxy creates a Toxiproxy proxy named after name if one does not
// already exist, so ApplyToxic has somewhere to attach a toxic. It binds the
// SAME port name declares, not an ephemeral one: this proxy is reached by a
// DNS alias pointed at the proxy container on the SUT-side network (see
// topology.go's overlayService doc comment), and the SUT still dials the
// port it always dialed — an ephemeral listen port would mean nothing ever
// actually connects to it (R-DC2-3's enforcement finding: a proxy that
// exists in Toxiproxy's control plane but isn't on the SUT's actual
// connection path is decorative).
//
// upstream is only used if this call actually creates the proxy (a 409 from
// an existing one is not an error, and its upstream is left as whatever it
// was already configured with — see ApplyToxic's call site, which always
// passes name as its own best-effort upstream guess since by that point
// EnsureProxies should already have created the real one, for both the
// external-host case (upstream == name, resolved via resolveUpstream) and
// the R-EXE-20 internal-dependency case (upstream is the renamed backend's
// host:port, which resolveUpstream correctly leaves alone: this process
// cannot resolve a Docker-internal-only compose service name, and Toxiproxy
// — itself on that network — resolves it directly instead).
func (a *ToxiproxyApplier) ensureProxy(name, upstream string) error {
	listen := "0.0.0.0:0"
	if _, port, err := net.SplitHostPort(name); err == nil && port != "" {
		listen = "0.0.0.0:" + port
	}
	body, status, err := a.do(http.MethodPost, "/proxies", map[string]any{
		"name":     name,
		"listen":   listen,
		"upstream": resolveUpstream(upstream),
	})
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusCreated || status == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("run: toxiproxy: create proxy %s: status %d: %s", name, status, body)
}

// resolveUpstream returns the address Toxiproxy should actually forward to.
// target is usually a classified hostname (e.g. "api.stripe.com:443") that
// topology.go also aliases, on the SUT-side network, to the proxy container
// itself (so the SUT dials the proxy instead of nowhere — see
// overlayService's doc comment). The proxy container is *itself* a member
// of that network, so if it resolved target's hostname the same way — via
// Docker's embedded DNS — it would resolve to itself: a self-loop, not the
// real destination. Resolving here instead, in this process (the
// orchestrator, running on the developer's host or wherever `tortureu run`
// executes — never inside the sandboxed SUT-side network), uses this
// process's own normal DNS resolution, which has no such alias and reaches
// the real host. If target's host is already an IP literal, or resolution
// fails, it is passed through unchanged (Toxiproxy then reports its own
// clear error rather than this package guessing).
func resolveUpstream(target string) string {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return target
	}
	if net.ParseIP(host) != nil {
		return target
	}
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		return target
	}
	return net.JoinHostPort(addrs[0], port)
}

// EnsureProxies creates a Toxiproxy proxy for every entry in targets up
// front, before load starts — not lazily, only when a fault happens to
// target one. A classified host with no fault at all must still never be
// reachable directly (R-DC2-2/R-DC2-3): if nothing creates its proxy until
// a fault fires, the window before that fault (or the entire run, for a
// host no fault ever targets) has no interception at all despite the DNS
// alias pointing there.
//
// Each key is the identifier the SUT actually dials — either a classified
// external "host:port" (R-DC2-3) or an internal dependency's original
// "host:port" (R-EXE-20) — and each value is where the proxy should
// actually forward to: the same string for an external host, or the
// R-EXE-20 renamed backend's "host:port" for an internal one.
func (a *ToxiproxyApplier) EnsureProxies(targets map[string]string) error {
	for name, upstream := range targets {
		if err := a.ensureProxy(name, upstream); err != nil {
			return fmt.Errorf("run: toxiproxy: EnsureProxies(%s): %w", name, err)
		}
	}
	return nil
}

// ApplyToxic implements fault.Applier: it ensures a proxy exists for the
// fault's target, then either disables the proxy (t.Disable, R-CFG-14's
// down: verb — connection refused) or adds t as a named toxic, returning an
// undo that reverses whichever it did.
func (a *ToxiproxyApplier) ApplyToxic(name string, t fault.Toxic) (func() error, error) {
	target, ok := a.targetFor(name)
	if !ok {
		// Falling back to name (the fault's own name, not a real host) would
		// silently proxy the wrong thing — the review finding this closes.
		return nil, fmt.Errorf("run: toxiproxy: fault %q has no registered target (RegisterTarget was never called for it)", name)
	}
	if err := a.ensureProxy(target, target); err != nil {
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
