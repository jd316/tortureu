package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jdb316/tortureu/internal/k6"
)

// fakeK6Script writes a shell script standing in for the k6 binary: it
// prints two phase-marker lines (internal/k6's own format, so
// k6.ParsePhaseMarker recognizes them exactly as it would for real k6
// output) and writes a summary JSON file at the --summary-export path
// before exiting 0. This exercises K6Runner's real subprocess plumbing
// (piping stdout live, writing/reading a file, process lifecycle) without
// needing the actual k6 binary, which is not available in this environment.
func fakeK6Script(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "k6")
	script := `#!/bin/sh
# args: run --summary-export <file> <script>
summary="$3"
echo "` + k6.PhaseMarkerPrefix + ` ramp_up 0"
echo "` + k6.PhaseMarkerPrefix + ` peak 1000"
echo '{"metrics":{"http_reqs":{"values":{"rate":10}}}}' > "$summary"
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// spec: R-EXE-8
func TestK6Runner_EmitsPhaseMarkersFromStdout(t *testing.T) {
	dir := t.TempDir()
	r := K6Runner{Bin: fakeK6Script(t), Dir: dir}

	handle, err := r.Start("// script")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	var got []string
	timeout := time.After(5 * time.Second)
	for len(got) < 2 {
		select {
		case m, ok := <-handle.Markers():
			if !ok {
				t.Fatalf("markers channel closed early, got %v", got)
			}
			got = append(got, m.Phase)
		case <-timeout:
			t.Fatalf("timed out waiting for markers, got %v", got)
		}
	}
	if got[0] != "ramp_up" || got[1] != "peak" {
		t.Errorf("markers = %v, want [ramp_up peak]", got)
	}
}

// spec: R-VER-10
func TestK6Runner_DoneCarriesSummaryJSONForIngestSummary(t *testing.T) {
	dir := t.TempDir()
	r := K6Runner{Bin: fakeK6Script(t), Dir: dir}

	handle, err := r.Start("// script")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case result := <-handle.Done():
		metrics, err := k6.IngestSummary(result.SummaryJSON)
		if err != nil {
			t.Fatalf("IngestSummary: %v", err)
		}
		if _, ok := metrics["http_reqs"]; !ok {
			t.Errorf("metrics = %v, want http_reqs", metrics)
		}
	case err := <-handle.Err():
		t.Fatalf("Err() = %v, want a result on Done()", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Done()")
	}
}

// spec: R-VER-2
func TestK6Runner_MissingBinaryReportsActionableError(t *testing.T) {
	// Since we do not bundle k6 (R-LIC-1), "k6 not on PATH" is the most
	// likely first-run outcome for anyone trying this tool. The bare
	// `exec: "k6": executable file not found in $PATH` os/exec produces
	// does not tell a new user what to do; this must say so explicitly
	// (R-VER-2: status:error means the tool broke, and the message is what
	// lets a user tell that apart from status:fail).
	r := K6Runner{Bin: filepath.Join(t.TempDir(), "definitely-not-a-real-k6-binary")}

	_, err := r.Start("// script")
	if err == nil {
		t.Fatal("Start returned nil error for a nonexistent binary")
	}
	if !strings.Contains(err.Error(), "k6 not found") {
		t.Errorf("error = %q, want it to say plainly that k6 was not found, not just relay os/exec's raw message", err.Error())
	}
	// Wrap, don't replace: the underlying detail must still be present for
	// a cause this package didn't anticipate.
	if !strings.Contains(err.Error(), "not-a-real-k6-binary") {
		t.Errorf("error = %q, want the underlying os/exec error preserved, not discarded", err.Error())
	}
}
