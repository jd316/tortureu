package emit

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jd316/tortureu/internal/config"
)

// hostPort splits a torture.yaml fault target ("postgres:5432",
// "api.stripe.com", or a bare compose service name) into a host and an
// optional port. port is "" when target carries none.
func hostPort(target string) (host, port string) {
	host, port, ok := strings.Cut(target, ":")
	if !ok {
		return target, ""
	}
	return host, port
}

// egressClass reports the class: of target per cfg.Egress.Hosts, and
// whether target was classified at all. A fault target not present in
// egress.hosts is legal (config.Parse also accepts the SUT's own service
// name), so the caller must handle !ok itself.
func egressClass(cfg *config.Config, target string) (class string, ok bool) {
	eh, present := cfg.Egress.Hosts[target]
	if !present {
		return "", false
	}
	return eh.Class, true
}

// resolveContainer decides which docker container's network namespace a
// network-fault command should run against, and what (if anything) scopes
// the effect to just the fault's declared target rather than the whole
// container.
//
//   - target == the SUT service: the fault is about the SUT's own
//     interface. container is the SUT itself; scope is "" (nothing to
//     narrow — it already IS the whole thing being impaired).
//   - target is a compose-internal dependency (egress class "internal",
//     e.g. postgres, redis): by docker-compose convention the hostname
//     before the colon is also the container name, so we can act on that
//     dependency's own container directly. scope is "".
//   - anything else (an external host: class mock/real/block, or a target
//     not listed in egress.hosts at all): there is no local container for
//     that peer. We act on the SUT container instead and — for tools that
//     support it — scope the effect to just that destination.
func resolveContainer(cfg *config.Config, target string) (container, scope string) {
	if target == cfg.Target.Service {
		return target, ""
	}
	host, _ := hostPort(target)
	if class, ok := egressClass(cfg, target); ok && class == "internal" {
		return host, ""
	}
	return cfg.Target.Service, host
}

// atComment renders a fault's at:/for: window as a comment, since no
// delegate-tier tool in this package schedules against the k6 phase clock
// (R-EXE-8) — that scheduling is `run`'s job, not a handed-off command's.
func atComment(f config.Fault) string {
	window := f.At
	if f.For != "" {
		window += " for " + f.For
	}
	return fmt.Sprintf("# fault %q: run this at %s (this emit does not schedule it — see the package doc)\n", f.Name, window)
}

// skipComment records, for a fault this tool does not translate, which
// verb it was and why — so an unsupported fault is visible in the output
// rather than silently missing from it (mirrors R-COV-6: unevaluable is
// reported, never silently treated as absent).
func skipComment(tool string, f config.Fault, reason string) string {
	return fmt.Sprintf("# fault %q (inject: %s): not translated by %s emit — %s\n", f.Name, f.Verb, tool, reason)
}

// basePort extracts the port from target.base_url ("http://host:8080" ->
// "8080"), used only by iptables' self-target (SUT-refuses-its-own-
// listener) case. "" when base_url has no explicit port.
func basePort(baseURL string) string {
	_, after, ok := strings.Cut(baseURL, "://")
	if !ok {
		after = baseURL
	}
	hostport, _, _ := strings.Cut(after, "/")
	_, port, ok := strings.Cut(hostport, ":")
	if !ok {
		return ""
	}
	if _, err := strconv.Atoi(port); err != nil {
		return ""
	}
	return port
}
