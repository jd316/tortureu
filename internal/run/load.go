// K6Runner is the real LoadRunner: it writes internal/k6.Compile's script to
// a temp file, runs k6 as a separate process (R-LIC-1: never linked into
// this binary), and turns its stdout into the PhaseMarker events the fault
// scheduler follows (R-EXE-8) — this is the "single shared clock" of
// R-SCOPE-2/R-EXE-1: k6's clock, read as it happens, not reconstructed from
// a declared schedule.
package run

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jdb316/tortureu/internal/k6"
)

// K6Runner starts a k6 process.
type K6Runner struct {
	// Bin is the k6 binary; defaults to "k6".
	Bin string
	// Dir is where the script and summary files are written; defaults to
	// os.TempDir().
	Dir string
}

func (r K6Runner) bin() string {
	if r.Bin != "" {
		return r.Bin
	}
	return "k6"
}

func (r K6Runner) dir() string {
	if r.Dir != "" {
		return r.Dir
	}
	return os.TempDir()
}

type k6Handle struct {
	markers chan PhaseMarker
	done    chan LoadResult
	errCh   chan error
}

func (h *k6Handle) Markers() <-chan PhaseMarker { return h.markers }
func (h *k6Handle) Done() <-chan LoadResult     { return h.done }
func (h *k6Handle) Err() <-chan error           { return h.errCh }

// Start writes script to a temp .js file and runs `k6 run --summary-export
// <file> <script>`, scanning stdout for internal/k6's phase-marker lines
// (R-EXE-8) as they're written — not after the process exits, so the fault
// scheduler can react while the run is still in progress.
func (r K6Runner) Start(script string) (LoadHandle, error) {
	scriptPath := filepath.Join(r.dir(), "tortureu-script.js")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		return nil, err
	}
	summaryPath := filepath.Join(r.dir(), "tortureu-summary.json")
	_ = os.Remove(summaryPath)

	cmd := exec.Command(r.bin(), "run", "--summary-export", summaryPath, scriptPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	h := &k6Handle{
		markers: make(chan PhaseMarker, 16),
		done:    make(chan LoadResult, 1),
		errCh:   make(chan error, 1),
	}

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if phase, ok := k6.ParsePhaseMarker(scanner.Text()); ok {
				h.markers <- PhaseMarker{Phase: phase, At: time.Now()}
			}
		}
		close(h.markers)

		waitErr := cmd.Wait()
		summary, readErr := os.ReadFile(summaryPath)
		if waitErr != nil {
			h.errCh <- waitErr
			return
		}
		if readErr != nil {
			h.errCh <- readErr
			return
		}
		h.done <- LoadResult{SummaryJSON: summary}
	}()

	return h, nil
}
