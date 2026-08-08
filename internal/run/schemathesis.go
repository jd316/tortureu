// schemathesis.go is the real Fuzzer behind `tortureu run --fuzz`
// (R-EXE-27): schemathesis, driven as a separate process against the SUT's
// own OpenAPI document while the load and the faults run.
//
// Everything encoded here was measured against schemathesis 4.24.3 (CLI
// name `st`) fuzzing a real HTTP service, not assumed:
//
//   - `st run <spec> -u <base-url>` is the invocation; a file-based schema
//     requires -u;
//   - the process exits 1 both for "the API broke a check" and for "no case
//     could be executed", and 2 for a usage error (measured: a spec path
//     that does not exist). Its exit status alone therefore cannot tell a
//     *result* from a *tool failure*, which is precisely the distinction
//     R-VER-2 forbids conflating;
//   - its JUnit report can: `<failure>` is a response that broke a check
//     (the SUT failing — a finding), `<error>` is a case that never ran at
//     all (measured against an unreachable port: "Network Error /
//     Connection failed"). So the report, not the exit code, decides.
package run

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// schemathesisInstallHint follows R-CLI-5's shape.
const schemathesisInstallHint = "install: pip install schemathesis (CLI name `st`) — https://schemathesis.readthedocs.io"

// schemathesisBins are the names the CLI is installed under, newest first.
var schemathesisBins = []string{"st", "schemathesis"}

// SchemathesisRunner drives schemathesis.
type SchemathesisRunner struct {
	// Bin overrides the binary; empty tries "st" then "schemathesis".
	Bin string
	// MaxExamples is schemathesis's -n (test cases per operation);
	// defaults to 20.
	MaxExamples int
	// Dir is where the JUnit report is written; defaults to os.TempDir().
	Dir string
}

// resolveBin returns the binary to run, or an error carrying an install
// hint when none is on PATH (R-CLI-5).
func (r SchemathesisRunner) resolveBin() (string, error) {
	candidates := schemathesisBins
	if r.Bin != "" {
		candidates = []string{r.Bin}
	}
	for _, c := range candidates {
		if _, err := exec.LookPath(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("--fuzz needs schemathesis and none of %v is on PATH — %s", candidates, schemathesisInstallHint)
}

// Preflight reports a missing schemathesis before the run resets anything
// (R-EXE-27, R-CLI-5).
func (r SchemathesisRunner) Preflight() error {
	_, err := r.resolveBin()
	return err
}

func (r SchemathesisRunner) maxExamples() int {
	if r.MaxExamples > 0 {
		return r.MaxExamples
	}
	return 20
}

func (r SchemathesisRunner) dir() string {
	if r.Dir != "" {
		return r.Dir
	}
	return os.TempDir()
}

// Start begins one fuzz pass. max bounds its own lifetime so a fuzzer that
// outlives this process still stops (R-EXE-27).
func (r SchemathesisRunner) Start(specPath, baseURL string, max time.Duration) (FuzzHandle, error) {
	bin, err := r.resolveBin()
	if err != nil {
		return nil, err
	}
	report := filepath.Join(r.dir(), fmt.Sprintf("tortureu-fuzz-%d.xml", time.Now().UnixNano()))
	cmd := exec.Command(bin, "run", specPath,
		"-u", baseURL,
		"-n", strconv.Itoa(r.maxExamples()),
		"--report", "junit",
		"--report-junit-path", report)
	buf := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = buf, buf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("schemathesis: %w", err)
	}

	h := &schemathesisHandle{cmd: cmd, out: buf, report: report, exited: make(chan struct{})}
	go func() {
		h.waitErr = cmd.Wait()
		close(h.exited)
	}()
	// The upper bound is a backstop, not the normal path: Run stops the
	// fuzzer when the load ends (R-EXE-27). Without it, a fuzz pass that
	// hangs would outlive the run that started it.
	h.bound = time.AfterFunc(max, func() {
		select {
		case <-h.exited:
		default:
			_ = cmd.Process.Kill()
		}
	})
	return h, nil
}

type schemathesisHandle struct {
	cmd    *exec.Cmd
	out    *syncBuffer
	report string
	bound  *time.Timer

	exited  chan struct{}
	waitErr error

	once   sync.Once
	result FuzzResult
}

// Stop ends the fuzz pass and reads its JUnit report. Idempotent, because
// teardownAll can reach it from several exit paths (R-EXE-5).
func (h *schemathesisHandle) Stop() FuzzResult {
	h.once.Do(func() {
		if h.bound != nil {
			h.bound.Stop()
		}
		cutShort := false
		select {
		case <-h.exited:
		default:
			cutShort = true
			_ = h.cmd.Process.Kill()
			<-h.exited
		}
		defer os.Remove(h.report)

		failures, unexecuted, err := parseJUnitReport(h.report)
		h.result = FuzzResult{Failures: failures, Unexecuted: unexecuted, CutShort: cutShort}
		switch {
		case err != nil && cutShort:
			// Killed before it wrote a report: nothing was measured, and
			// saying so is the honest answer (R-EXE-27's "cut short").
			h.result.Failures = nil
		case err != nil:
			// No report and we did not kill it: schemathesis could not run
			// at all — a tool failure (R-VER-2's error), quoting its own
			// output so the user is not sent to debug TortureU.
			h.result.Err = fmt.Errorf("schemathesis produced no report: %v: %s", err, strings.TrimSpace(lastLines(h.out.String(), 3)))
		case len(failures) == 0 && unexecuted > 0:
			// Every case errored out before producing a verdict: the target
			// was unreachable or the spec unusable, so there is no result
			// to report — status error, never a silent clean run.
			h.result.Err = fmt.Errorf("schemathesis executed no test case successfully (%d network/setup error(s)): %s", unexecuted, strings.TrimSpace(lastLines(h.out.String(), 3)))
		}
	})
	return h.result
}

// junitSuites is the subset of schemathesis's JUnit report this package
// reads. The failure/error split is the whole reason the report is parsed
// at all rather than the exit status trusted (see the file comment).
type junitSuites struct {
	XMLName xml.Name     `xml:"testsuites"`
	Suites  []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Cases []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name     string   `xml:"name,attr"`
	Failures []string `xml:"failure"`
	Errors   []string `xml:"error"`
}

// parseJUnitReport returns the SUT's failures (results, R-VER-2) and the
// count of cases that never executed (not results at all).
func parseJUnitReport(path string) ([]FuzzFailure, int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	var suites junitSuites
	if err := xml.Unmarshal(raw, &suites); err != nil {
		return nil, 0, fmt.Errorf("parse JUnit report: %w", err)
	}
	var failures []FuzzFailure
	unexecuted := 0
	for _, s := range suites.Suites {
		for _, c := range s.Cases {
			for _, f := range c.Failures {
				failures = append(failures, FuzzFailure{Operation: c.Name, Detail: summarizeFailure(f)})
			}
			unexecuted += len(c.Errors)
		}
	}
	return failures, unexecuted, nil
}

// summarizeFailure reduces one JUnit failure body to a single short line.
// The body carries the whole response (schemathesis embeds the HTML error
// page it got back); a verdict finding needs the check that broke, not the
// page.
func summarizeFailure(body string) string {
	var parts []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if line == "" || strings.HasPrefix(line, "<") || strings.HasPrefix(line, "`<") {
			continue
		}
		parts = append(parts, line)
		if len(parts) == 3 {
			break
		}
	}
	s := strings.Join(parts, "; ")
	if len(s) > 240 {
		s = s[:240] + "..."
	}
	return s
}
