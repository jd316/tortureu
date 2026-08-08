package detect

import (
	"bufio"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// bareHostStructureRe matches a scheme-less value that is, in its entirety,
// a dotted token with an optional port — e.g. "api.partner.com",
// "api.partner.com:8443", but also "config.yaml" or "3.11.4". Structure
// alone cannot tell a hostname from a filename or a version string, so a
// match here is only a candidate: commonTLDs below decides whether it's
// actually a host. It deliberately requires the WHOLE value to match: an
// env var value that is merely "host-shaped" as a substring (a path, a
// sentence) is not a host, and R-DET-4's discovery must not guess. Plain
// IPv4-shaped values fail the commonTLDs check the same way a version
// string does, since neither ends in a real TLD label.
var bareHostStructureRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+(:[0-9]{1,5})?$`)

// commonTLDs is a small allowlist of top-level labels that make a
// structurally host-shaped value look like a genuine internet hostname
// rather than a filename or a version string.
//
// This is a heuristic, not a spec, and it is deliberately narrow: real
// filenames routinely end in a short alphabetic label too (config.yaml,
// ca.pem, app.js, schema.sql, Dockerfile.prod), and a permissive check that
// accepted any letters-only final label misclassified every one of them as
// an external host — which is worse than missing a real one, since an
// invented unclassified host aborts every run that has a config file in an
// env var (nearly all of them). Prefer missing a host over inventing one:
// an obscure or private TLD not in this list is simply not discovered here,
// same as any other value this package chooses not to guess about.
var commonTLDs = map[string]bool{
	"com": true, "net": true, "org": true, "io": true, "dev": true,
	"co": true, "app": true, "ai": true, "cloud": true, "tech": true,
	"systems": true, "services": true, "info": true, "biz": true, "me": true,
}

// loopbackHosts are never external, regardless of what other checks say.
var loopbackHosts = map[string]bool{
	"localhost": true,
	"127.0.0.1": true,
	"0.0.0.0":   true,
}

// extractHost reports the host (optionally "host:port") a single env var
// value names, and whether it names one at all. It recognizes two shapes
// only: a URL with a network scheme ("redis://cache:6379",
// "https://api.stripe.com/v1"), and a bare dotted hostname
// ("api.partner.com", "api.partner.com:8443"). Anything else is left alone
// rather than guessed at (R-DET-4).
func extractHost(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.Hostname() == "" {
			return "", false
		}
		host := u.Hostname()
		if u.Port() != "" {
			host += ":" + u.Port()
		}
		return host, true
	}
	if bareHostStructureRe.MatchString(value) {
		labels := strings.Split(hostOf(value), ".")
		tld := strings.ToLower(labels[len(labels)-1])
		if commonTLDs[tld] {
			return value, true
		}
	}
	return "", false
}

// hostOf strips a trailing ":port" from a "host" or "host:port" string.
func hostOf(s string) string {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i]
	}
	return s
}

// detectExternalHosts implements the R-DET-4 half detection previously
// skipped: every host named in a compose service's environment: or
// env_file — the realistic way an app is told which external partner to
// call — that is not itself an in-compose service is an external host,
// recorded in sys.Egress and classified "unclassified" (default-deny,
// R-DC2-1). internalNames is every compose service name (SUT and
// dependencies alike): a value that merely names another service in the
// same compose file is an internal reference, not an external host, and
// MUST NOT be reclassified.
func detectExternalHosts(services map[string]types.ServiceConfig, workingDir string, internalNames map[string]bool, sys *System) {
	seen := map[string]bool{}
	for _, addr := range sys.Egress {
		seen[addr] = true
	}

	record := func(host string) {
		if seen[host] {
			return
		}
		if loopbackHosts[hostOf(host)] || internalNames[hostOf(host)] {
			return
		}
		seen[host] = true
		sys.Egress = append(sys.Egress, host)
		if sys.EgressClass == nil {
			sys.EgressClass = map[string]string{}
		}
		sys.EgressClass[host] = "unclassified"
	}

	for _, svc := range services {
		for _, v := range svc.Environment {
			if v == nil {
				continue
			}
			if host, ok := extractHost(*v); ok {
				record(host)
			}
		}
		for _, ef := range svc.EnvFiles {
			for _, value := range readEnvFile(ef.Path, workingDir) {
				if host, ok := extractHost(value); ok {
					record(host)
				}
			}
		}
	}
}

// readEnvFile reads KEY=VALUE lines from a compose env_file (path resolved
// against workingDir if relative), returning the values. Malformed or
// missing files yield no values rather than an error — an env_file that
// doesn't parse is not this function's problem to solve, and a required
// env_file that is genuinely missing will already fail loudly at `docker
// compose up`.
func readEnvFile(path, workingDir string) []string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(workingDir, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var values []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		_, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		values = append(values, v)
	}
	return values
}
