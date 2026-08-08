package doctor

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// sourceSite is a known client library's known construction call — the
// bounded surface R-AUD-5 permits the audit to read. Adding a library here
// is a deliberate, reviewed decision, mirroring internal/detect's own
// client-pattern tables (SPEC.md §3.1). A dependency type absent from this
// table is never source-inspected; Audit reports "not determined" for it
// instead (R-AUD-6).
type sourceSite struct {
	depType string

	// constructors identifies a call that unambiguously builds this
	// depType's client (bounded "construction site" per R-AUD-5) — the
	// function name alone names the driver, e.g. "pgxpool.New".
	constructors []string

	// sharedConstructors identifies a call that builds a client through a
	// driver-generic API (e.g. "sql.Open", which database/sql exposes for
	// every SQL driver). A shared constructor is only attributed to
	// depType when the same file also contains one of driverSigs —
	// without that corroboration, which dependency the call belongs to is
	// not knowable, and R-AUD-6 requires "not determined" over a guess.
	sharedConstructors []string
	driverSigs         []string

	// timeoutSigs, if found anywhere in the file containing a constructor
	// call, are evidence a timeout is configured.
	timeoutSigs []string

	// retrySigs, capSigs, backoffSigs, jitterSigs are evidence of a retry
	// mechanism, and of its cap/backoff/jitter respectively (R-AUD-2).
	retrySigs   []string
	capSigs     []string
	backoffSigs []string
	jitterSigs  []string
}

// goSourceSites covers the Go client libraries internal/detect already
// recognizes (lockfile.go's goClientPatterns) for which a timeout/retry
// signal is knowable from the constructor's own file without following
// arbitrary control flow.
var goSourceSites = []sourceSite{
	{
		depType:            "postgresql",
		constructors:       []string{"pgxpool.New", "pgxpool.NewWithConfig", "pgx.Connect"},
		sharedConstructors: []string{"sql.Open"},
		driverSigs:         []string{"github.com/lib/pq", "github.com/jackc/pgx"},
		timeoutSigs:        []string{"ConnectTimeout", "context.WithTimeout", "context.WithDeadline", "StatementTimeout"},
		retrySigs:          []string{"Retry", "retry", "Backoff", "backoff"},
		capSigs:            []string{"MaxRetries", "MaxTries", "MaxElapsedTime"},
		backoffSigs:        []string{"Backoff", "backoff"},
		jitterSigs:         []string{"Jitter", "jitter"},
	},
	{
		depType:      "redis",
		constructors: []string{"redis.NewClient"},
		timeoutSigs:  []string{"DialTimeout", "ReadTimeout", "WriteTimeout", "context.WithTimeout"},
		retrySigs:    []string{"Retry", "retry", "Backoff", "backoff"},
		capSigs:      []string{"MaxRetries"},
		backoffSigs:  []string{"MinRetryBackoff", "MaxRetryBackoff"},
		jitterSigs:   []string{"Jitter", "jitter"},
	},
	{
		depType:            "mysql",
		sharedConstructors: []string{"sql.Open"},
		driverSigs:         []string{"github.com/go-sql-driver/mysql"},
		timeoutSigs:        []string{"Timeout", "ReadTimeout", "WriteTimeout", "context.WithTimeout"},
		retrySigs:          []string{"Retry", "retry", "Backoff", "backoff"},
		capSigs:            []string{"MaxRetries", "MaxTries"},
		backoffSigs:        []string{"Backoff", "backoff"},
		jitterSigs:         []string{"Jitter", "jitter"},
	},
	{
		// http is Go's stdlib net/http (TBD-10): it never appears in a
		// go.mod require line, so R-DET-5 can never attach it to a
		// dependency's Clients — this is the one site Audit checks even
		// when Clients is empty (see Audit's doc comment). timeoutSigs are
		// the realistic source forms of the knobs internal/run's own
		// candidate table already names for net/http (Client.Timeout,
		// Transport.ResponseHeaderTimeout, Transport.TLSHandshakeTimeout):
		// a struct literal or assignment sets the bare field name
		// ("Timeout:", "ResponseHeaderTimeout"), never the qualified
		// "Client.Timeout" form, which only appears in documentation.
		depType:      "http",
		constructors: []string{"http.Client{", "&http.Client{", "http.Transport{"},
		timeoutSigs:  []string{"Timeout:", "ResponseHeaderTimeout", "TLSHandshakeTimeout", "context.WithTimeout", "context.WithDeadline"},
		retrySigs:    []string{"Retry", "retry", "Backoff", "backoff"},
		capSigs:      []string{"MaxRetries", "MaxTries", "MaxElapsedTime"},
		backoffSigs:  []string{"Backoff", "backoff"},
		jitterSigs:   []string{"Jitter", "jitter"},
	},
}

// siteHasEvidence reports whether depType's known construction site was
// actually found under dir. Audit uses this to decide whether the net/http
// fallback check (which, unlike a lockfile-sourced client, has no manifest
// signal correlating it to any particular dependency) is worth reporting
// at all — silence when there is no evidence net/http is used anywhere is
// the honest answer, not a "not determined" finding on every dependency
// that happens to have no lockfile client (R-AUD-6 reserves "not
// determined" for a library known to be in use whose setting couldn't be
// resolved, not for a library with no evidence of use at all).
func siteHasEvidence(dir, depType string) bool {
	site, ok := siteFor(depType)
	if !ok {
		return false
	}
	_, found := findConstructionSite(dir, site)
	return found
}

func siteFor(depType string) (sourceSite, bool) {
	for _, s := range goSourceSites {
		if s.depType == depType {
			return s, true
		}
	}
	return sourceSite{}, false
}

// inspectResult is what bounded source inspection could determine about
// one resilience knob (R-AUD-6: "not determined" and "not configured" are
// different outcomes).
type inspectResult struct {
	determined bool
	present    bool
	reason     string // populated when !determined
}

// inspectTimeout performs bounded, table-driven inspection (R-AUD-5) of
// depType's known construction site under dir, and reports whether a
// timeout could be confirmed configured (R-AUD-1).
func inspectTimeout(dir, depType string) inspectResult {
	site, text, ok, reason := locate(dir, depType)
	if !ok {
		return inspectResult{reason: reason}
	}
	return inspectResult{determined: true, present: containsAny(text, site.timeoutSigs)}
}

// inspectRetry performs bounded, table-driven inspection (R-AUD-5) of
// depType's known construction site under dir, and reports whether retry
// is configured with a cap, backoff, and jitter (R-AUD-2). A file that
// shows no retry mechanism at all is a determined "not configured", since
// it was actually read — not a guess.
func inspectRetry(dir, depType string) inspectResult {
	site, text, ok, reason := locate(dir, depType)
	if !ok {
		return inspectResult{reason: reason}
	}
	if !containsAny(text, site.retrySigs) {
		return inspectResult{determined: true, present: false}
	}
	complete := containsAny(text, site.capSigs) &&
		containsAny(text, site.backoffSigs) &&
		containsAny(text, site.jitterSigs)
	return inspectResult{determined: true, present: complete}
}

// locate finds depType's construction-site table entry and the file (if
// any, under dir) where its constructor is actually called.
func locate(dir, depType string) (site sourceSite, text string, ok bool, reason string) {
	site, known := siteFor(depType)
	if !known {
		return sourceSite{}, "", false, "no known construction site in doctor's table for dependency type " + depType
	}
	text, found := findConstructionSite(dir, site)
	if !found {
		return sourceSite{}, "", false, "construction call for " + depType + " not found in source"
	}
	return site, text, true, ""
}

// findConstructionSite walks dir's Go source for a file attributable to
// site, and returns that file's content — the bounded inspection window
// (R-AUD-5: the construction site, not the whole program).
//
// A file matches on an unambiguous constructor alone. A shared,
// driver-generic constructor (e.g. "sql.Open") only matches when the same
// file also contains one of site's driverSigs — otherwise which dependency
// the call belongs to is not knowable, and this reports no match so the
// caller falls back to "not determined" (R-AUD-6) rather than attributing
// the file to the wrong dependency.
func findConstructionSite(dir string, site sourceSite) (string, bool) {
	if dir == "" {
		return "", false
	}
	var text string
	var found bool
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "vendor" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := string(raw)
		attributable := containsAny(content, site.constructors) ||
			(containsAny(content, site.sharedConstructors) && containsAny(content, site.driverSigs))
		if attributable {
			text = content
			found = true
		}
		return nil
	})
	return text, found
}

func containsAny(text string, subs []string) bool {
	for _, s := range subs {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}
