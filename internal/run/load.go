// K6Runner is the real LoadRunner: it writes internal/k6.Compile's script to
// a temp file, runs k6 as a separate process (R-LIC-1: never linked into
// this binary), and turns its stdout into the PhaseMarker events the fault
// scheduler follows (R-EXE-8) — this is the "single shared clock" of
// R-SCOPE-2/R-EXE-1: k6's clock, read as it happens, not reconstructed from
// a declared schedule.
//
// Container mode (SetSUTContainer, R-DC2-3 fix): R-DC2-3 puts the SUT on an
// internal:true Docker network with no route out — and, it turns out,
// Docker also does not publish host ports for a container whose only
// network is internal. A host-process k6 dialing the SUT's own
// `target.base_url` (typically "http://localhost:<port>", a published
// port) got connection-refused on every single request, for every run —
// found empirically by this task's eval, which drives real fixtures rather
// than fakes. DC-2 isolation and the load path were, as first built,
// mutually exclusive.
//
// The fix does not touch the SUT's own network topology (it stays
// exclusively on the internal network — the actual DC-2 guarantee is
// unchanged) and does not require rewriting `target.base_url` or the
// compiled k6 script (internal/k6.Compile is read-only for this task, and
// bakes base_url in verbatim). Instead, when Run discovers the SUT's
// container (see run.go's discoverSUTContainer), k6 itself runs as a
// container joining that container's network *namespace*
// (`docker run --network container:<id>`) rather than as a host process.
// Inside that namespace, "localhost:<port>" *is* the SUT's own loopback —
// the identical address the SUT's own base_url almost always already
// names, unchanged, because the SUT's own process listens there. This is
// still R-LIC-1-compliant: it is another form of "a process invoked
// separately," not linking k6 into this binary.
//
// Container mode moves the script in and the summary out via `docker cp`
// (create, cp, start -a, cp, rm) rather than a bind mount: some Docker
// setups (this task's own sandbox among them) restrict bind mounts to an
// explicitly allow-listed set of host paths, and rejecting an otherwise
// working run over that would be its own new failure mode.
package run

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jdb316/tortureu/internal/k6"
)

// k6Image is the default k6 container image used in container mode. Pinned
// for reproducibility; SPEC.md does not name one (escalated in the Task 7
// report alongside this package's other placeholder pins, e.g.
// topology.go's proxyImage).
const k6Image = "grafana/k6:0.54.0"

// k6ContainerScriptPath/k6ContainerSummaryPath are where the script and
// summary live inside the k6 container in container mode. /tmp rather than
// / : the real grafana/k6 image runs as a non-root user with no write
// access to /, discovered running this end to end against a real SUT
// ("Could not open '/tortureu-summary.json': permission denied") — /tmp is
// conventionally world-writable in every image this package has to work
// with, and needs no directory created first, since these are copied
// in/out via `docker cp` rather than a bind mount (see the package doc
// comment on why not a mount).
const (
	k6ContainerScriptPath  = "/tmp/tortureu-script.js"
	k6ContainerSummaryPath = "/tmp/tortureu-summary.json"
)

// K6Runner starts a k6 process, either as a host process (default) or,
// once SetSUTContainer has been called, as a container sharing the named
// container's network namespace (see the package doc comment).
type K6Runner struct {
	// Bin is the k6 binary for host-process mode; defaults to "k6".
	Bin string
	// Dir is where the script and summary files are written; defaults to
	// os.TempDir().
	Dir string
	// Image is the k6 container image used in container mode; defaults to
	// k6Image.
	Image string
	// DockerBin is the docker binary for container mode; defaults to
	// "docker".
	DockerBin string
	// ContainerArgs overrides the arguments run inside the k6 container
	// (after the image name). Tests substitute a shell script standing in
	// for the k6 CLI contract, the same pattern load_test.go's
	// fakeK6Script already uses for host-process mode, so this package's
	// real docker-create/cp/start plumbing is provable without the actual
	// k6 image. Defaults to k6's own `run --summary-export ... ...` args.
	ContainerArgs []string

	sutContainer string
}

// SetSUTContainer switches Start into container mode: k6 will run inside a
// container joining sutContainer's network namespace instead of as a host
// process. See the package doc comment for why this exists. Called by Run
// once it has discovered the SUT's actual container (run.go).
func (r *K6Runner) SetSUTContainer(name string) {
	r.sutContainer = name
}

func (r *K6Runner) bin() string {
	if r.Bin != "" {
		return r.Bin
	}
	return "k6"
}

func (r *K6Runner) dir() string {
	if r.Dir != "" {
		return r.Dir
	}
	return os.TempDir()
}

func (r *K6Runner) image() string {
	if r.Image != "" {
		return r.Image
	}
	return k6Image
}

func (r *K6Runner) dockerBin() string {
	if r.DockerBin != "" {
		return r.DockerBin
	}
	return "docker"
}

func (r *K6Runner) containerArgs() []string {
	if r.ContainerArgs != nil {
		return r.ContainerArgs
	}
	return []string{"run", "--summary-export", k6ContainerSummaryPath, k6ContainerScriptPath}
}

// wrapStartError turns cmd.Start()'s failure to launch bin into an
// actionable message (R-VER-2: status:error means the tool itself broke,
// and "k6 not found on PATH" is the difference between "go install k6" and
// "go debug your service"). Since we do not bundle k6 (R-LIC-1), this is
// the single most likely first-run failure for anyone trying the tool, and
// os/exec's own message ("exec: \"k6\": executable file not found in
// $PATH", or "fork/exec /path/to/k6: no such file or directory" when bin
// contains a path separator — two different stdlib error shapes for the
// same underlying problem) does not say what to do about it. Wraps, never
// replaces, the original error: the detail matters when the cause turns
// out not to be a missing binary at all.
func wrapStartError(bin string, err error) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("k6 not found on PATH (looked for %q) — install k6 (https://k6.io) or pass -k6-path: %w", bin, err)
	}
	return fmt.Errorf("start k6: %w", err)
}

type k6Handle struct {
	markers chan PhaseMarker
	done    chan LoadResult
	errCh   chan error
}

func (h *k6Handle) Markers() <-chan PhaseMarker { return h.markers }
func (h *k6Handle) Done() <-chan LoadResult     { return h.done }
func (h *k6Handle) Err() <-chan error           { return h.errCh }

func newK6Handle() *k6Handle {
	return &k6Handle{
		markers: make(chan PhaseMarker, 16),
		done:    make(chan LoadResult, 1),
		errCh:   make(chan error, 1),
	}
}

// scanMarkers reads stdout line by line, forwarding internal/k6's
// phase-marker lines (R-EXE-8) as they're written — not after the process
// exits, so the fault scheduler can react while the run is still in
// progress — and closes h.markers once stdout ends.
func scanMarkers(h *k6Handle, stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if phase, ok := k6.ParsePhaseMarker(scanner.Text()); ok {
			h.markers <- PhaseMarker{Phase: phase, At: time.Now()}
		}
	}
	close(h.markers)
}

// Start writes script to a temp .js file and either runs `k6 run
// --summary-export <file> <script>` directly (host-process mode) or the
// same k6 invocation inside a container sharing the SUT's network
// namespace (container mode, once SetSUTContainer has been called — see
// the package doc comment).
func (r *K6Runner) Start(script string) (LoadHandle, error) {
	scriptPath := filepath.Join(r.dir(), "tortureu-script.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		return nil, err
	}
	summaryPath := filepath.Join(r.dir(), "tortureu-summary.json")
	_ = os.Remove(summaryPath)

	if r.sutContainer != "" {
		return r.startContainer(scriptPath, summaryPath)
	}
	return r.startHostProcess(scriptPath, summaryPath)
}

func (r *K6Runner) startHostProcess(scriptPath, summaryPath string) (LoadHandle, error) {
	cmd := exec.Command(r.bin(), "run", "--summary-export", summaryPath, scriptPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, wrapStartError(r.bin(), err)
	}

	h := newK6Handle()
	go func() {
		scanMarkers(h, stdout)
		waitErr := cmd.Wait()
		if waitErr != nil {
			h.errCh <- waitErr
			return
		}
		summary, readErr := os.ReadFile(summaryPath)
		if readErr != nil {
			h.errCh <- readErr
			return
		}
		h.done <- LoadResult{SummaryJSON: summary}
	}()
	return h, nil
}

// startContainer runs k6 in a container sharing r.sutContainer's network
// namespace. It moves the script in and the summary out via `docker cp`
// rather than a bind mount (see the package doc comment on why), which
// means the container must be created (not started) first, so there is
// something to `cp` into before it runs.
func (r *K6Runner) startContainer(scriptPath, summaryPath string) (LoadHandle, error) {
	createArgs := append([]string{"create", "--network", "container:" + r.sutContainer, r.image()}, r.containerArgs()...)
	out, err := exec.Command(r.dockerBin(), createArgs...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("create k6 container: %w: %s", err, out)
	}
	containerID := strings.TrimSpace(string(out))
	// docker create can print more than just the ID (e.g. a pull progress
	// line) on some setups; the ID is always the last line.
	if lines := strings.Fields(containerID); len(lines) > 0 {
		containerID = lines[len(lines)-1]
	}

	cleanup := func() { exec.Command(r.dockerBin(), "rm", "-f", containerID).Run() }

	if out, err := exec.Command(r.dockerBin(), "cp", scriptPath, containerID+":"+k6ContainerScriptPath).CombinedOutput(); err != nil {
		cleanup()
		return nil, fmt.Errorf("copy script into k6 container: %w: %s", err, out)
	}

	cmd := exec.Command(r.dockerBin(), "start", "-a", containerID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return nil, err
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, fmt.Errorf("start k6 container: %w", err)
	}

	h := newK6Handle()
	go func() {
		scanMarkers(h, stdout)
		waitErr := cmd.Wait()
		cpErr := exec.Command(r.dockerBin(), "cp", containerID+":"+k6ContainerSummaryPath, summaryPath).Run()
		cleanup()
		if waitErr != nil {
			h.errCh <- waitErr
			return
		}
		if cpErr != nil {
			h.errCh <- fmt.Errorf("copy summary out of k6 container: %w", cpErr)
			return
		}
		summary, readErr := os.ReadFile(summaryPath)
		if readErr != nil {
			h.errCh <- readErr
			return
		}
		h.done <- LoadResult{SummaryJSON: summary}
	}()
	return h, nil
}
