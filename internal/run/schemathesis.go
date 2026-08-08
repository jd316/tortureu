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
//   - the same three hold for schemathesis's own official image, which is
//     the same 4.24.3 CLI; container mode (InNamespaceOf, TBD-12) therefore
//     changes only *where* the process runs, never how its output is read.
//     Measured against a real internal:true SUT: the namespace-joined pass
//     reaches it and reports its planted 500 as a `<failure>`, while from
//     the host there is no published port to connect to at all;
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
const schemathesisInstallHint = "install: pip install schemathesis (CLI name `st`) — https://schemathesis.readthedocs.io, or install Docker, which lets TortureU run schemathesis's own official image instead"

// schemathesisBins are the names the CLI is installed under, newest first.
var schemathesisBins = []string{"st", "schemathesis"}

// schemathesisImage is the official image used in container mode, pinned to
// the same version this file's behaviour was measured against rather than
// the floating `stable` tag (TBD-11's rule: no `latest`, in any spelling).
const schemathesisImage = "schemathesis/schemathesis:4.24.3"

// Where the spec goes in and the report comes out inside the container.
// /tmp for K6Runner's reason: the image runs as a non-root user, and /tmp
// is writable in every image this has to work with. Both are moved with
// `docker cp` rather than a bind mount, again for K6Runner's reason — some
// Docker setups restrict bind mounts to allow-listed host paths.
const (
	schemathesisContainerSpecBase = "/tmp/tortureu-fuzz-spec"
	schemathesisContainerReport   = "/tmp/tortureu-fuzz-report.xml"
)

// SchemathesisRunner drives schemathesis.
type SchemathesisRunner struct {
	// Bin overrides the binary; empty tries "st" then "schemathesis".
	Bin string
	// MaxExamples is schemathesis's -n (test cases per operation);
	// defaults to 20.
	MaxExamples int
	// Dir is where the JUnit report is written; defaults to os.TempDir().
	Dir string
	// Image is the container-mode image; defaults to schemathesisImage.
	Image string
	// DockerBin is the docker binary for container mode; defaults to
	// "docker".
	DockerBin string
	// Netns is the container whose network namespace the fuzzer joins when
	// base_url is not reachable from this host (R-EXE-27's Reach rule,
	// TBD-12) — the same container the load generator joins, so both are
	// pointed at the same thing. Empty means host-process mode only.
	Netns string
}

// InNamespaceOf returns a copy of r bound to container's network namespace,
// for the reason PgbenchRunner.InNamespaceOf gives.
func (r SchemathesisRunner) InNamespaceOf(container string) Fuzzer {
	r.Netns = container
	return r
}

func (r SchemathesisRunner) image() string {
	if r.Image != "" {
		return r.Image
	}
	return schemathesisImage
}

func (r SchemathesisRunner) dockerBin() string {
	if r.DockerBin != "" {
		return r.DockerBin
	}
	return "docker"
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
// (R-EXE-27, R-CLI-5). Docker satisfies it too, for the reason
// PgbenchRunner.Preflight gives: running the official image in the SUT's
// network namespace is the only route that reaches a SUT R-DC2-3 isolated,
// so a machine with Docker and no `st` is not a machine that must be
// refused.
func (r SchemathesisRunner) Preflight() error {
	if _, err := r.resolveBin(); err == nil {
		return nil
	}
	if _, err := exec.LookPath(r.dockerBin()); err == nil {
		return nil
	}
	_, err := r.resolveBin()
	return err
}

// useContainer is PgbenchRunner.useContainer's rule for the fuzzer: dial
// base_url from here first, and only join the SUT's namespace when that
// address has nothing listening on this host — which is what R-DC2-3's
// internal-only network guarantees. There is nothing to rewrite either way,
// so unlike the DB case there is no un-rewritable form to bail out on.
func (r SchemathesisRunner) useContainer(baseURL string) bool {
	if r.Netns == "" {
		return false
	}
	if _, err := r.resolveBin(); err != nil {
		return true
	}
	addr, ok := urlAddr(baseURL)
	if !ok {
		return false
	}
	return !hostCanReach(addr)
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
	if r.useContainer(baseURL) {
		return r.startContainer(specPath, baseURL, max)
	}
	return r.startHostProcess(specPath, baseURL, max)
}

// runArgs is schemathesis's own CLI contract, identical in both modes —
// spec is a path this process can see in host mode and a path inside the
// container in container mode, and report likewise.
func (r SchemathesisRunner) runArgs(spec, baseURL, report string) []string {
	return []string{"run", spec,
		"-u", baseURL,
		"-n", strconv.Itoa(r.maxExamples()),
		"--report", "junit",
		"--report-junit-path", report}
}

func (r SchemathesisRunner) startHostProcess(specPath, baseURL string, max time.Duration) (FuzzHandle, error) {
	bin, err := r.resolveBin()
	if err != nil {
		return nil, err
	}
	report := r.reportPath()
	return startSchemathesis(exec.Command(bin, r.runArgs(specPath, baseURL, report)...), report, "", r.dockerBin(), max)
}

// startContainer runs the same schemathesis CLI inside its official image,
// joined to r.Netns's network namespace — K6Runner's mechanism (load.go)
// applied to the fuzzer, including its create / cp-in / start -a / cp-out /
// rm shape, and for the same reason: `docker cp` works where a bind mount
// may be refused.
//
// baseURL is passed through untouched. That is the whole of R-EXE-27's
// Reach rule: inside this namespace it names exactly what it names for k6.
func (r SchemathesisRunner) startContainer(specPath, baseURL string, max time.Duration) (FuzzHandle, error) {
	// Keep the spec's extension: schemathesis picks its parser from it, and
	// a YAML document handed over as extensionless would be read as JSON.
	containerSpec := schemathesisContainerSpecBase + filepath.Ext(specPath)
	createArgs := append([]string{"create", "--network", "container:" + r.Netns, r.image()},
		r.runArgs(containerSpec, baseURL, schemathesisContainerReport)...)
	out, err := exec.Command(r.dockerBin(), createArgs...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("create schemathesis container in %s's network namespace: %w: %s", r.Netns, err, out)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return nil, fmt.Errorf("create schemathesis container: docker printed no container id")
	}
	containerID := fields[len(fields)-1]

	if out, err := exec.Command(r.dockerBin(), "cp", specPath, containerID+":"+containerSpec).CombinedOutput(); err != nil {
		_ = exec.Command(r.dockerBin(), "rm", "-f", containerID).Run()
		return nil, fmt.Errorf("copy the OpenAPI document into the schemathesis container: %w: %s", err, out)
	}

	h, err := startSchemathesis(exec.Command(r.dockerBin(), "start", "-a", containerID), r.reportPath(), containerID, r.dockerBin(), max)
	if err != nil {
		_ = exec.Command(r.dockerBin(), "rm", "-f", containerID).Run()
		return nil, err
	}
	return h, nil
}

// reportPath is where the JUnit report lands on this host — written
// directly by a host process, copied out of the container otherwise.
func (r SchemathesisRunner) reportPath() string {
	return filepath.Join(r.dir(), fmt.Sprintf("tortureu-fuzz-%d.xml", time.Now().UnixNano()))
}

// startSchemathesis launches cmd and wires the handle both modes share.
func startSchemathesis(cmd *exec.Cmd, report, containerID, dockerBin string, max time.Duration) (FuzzHandle, error) {
	buf := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = buf, buf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("schemathesis: %w", err)
	}

	h := &schemathesisHandle{cmd: cmd, out: buf, report: report, exited: make(chan struct{}),
		containerID: containerID, dockerBin: dockerBin}
	go func() {
		h.waitErr = cmd.Wait()
		close(h.exited)
	}()
	// The upper bound is a backstop, not the normal path: Run stops the
	// fuzzer when the load ends (R-EXE-27). Without it, a fuzz pass that
	// hangs would outlive the run that started it — and in container mode
	// killing the `docker start` client alone would not stop the container,
	// so the container is removed too.
	h.bound = time.AfterFunc(max, func() {
		select {
		case <-h.exited:
		default:
			h.kill()
		}
	})
	return h, nil
}

type schemathesisHandle struct {
	cmd    *exec.Cmd
	out    *syncBuffer
	report string
	bound  *time.Timer
	// containerID is set in container mode only; dockerBin is how it is
	// reached.
	containerID string
	dockerBin   string

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
			h.kill()
			<-h.exited
		}
		// Container mode: the report is inside the container, so it has to
		// come out before the container goes away — including after a kill,
		// since schemathesis writes the report as it exits and a cut-short
		// pass may still have written one.
		if h.containerID != "" {
			_ = exec.Command(h.dockerBin, "cp", h.containerID+":"+schemathesisContainerReport, h.report).Run()
			// Unconditional, on every exit path: nothing this run started
			// may outlive it (R-EXE-5).
			_ = exec.Command(h.dockerBin, "rm", "-f", h.containerID).Run()
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

// kill ends the fuzz pass now. In container mode killing the attached
// `docker start` client is not enough — the container it is attached to
// keeps running — so the container is killed too. Killed, not removed:
// whatever report it managed to write still has to be copied out of it
// afterwards, and removing it here would silently throw away the findings
// of a pass the run merely cut short. Stop removes it once it has the file.
func (h *schemathesisHandle) kill() {
	if h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	if h.containerID != "" {
		_ = exec.Command(h.dockerBin, "kill", h.containerID).Run()
	}
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
