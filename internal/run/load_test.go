package run

import (
	"os"
	"os/exec"
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

// fakeK6ScriptStderrMarkers writes a shell script standing in for the k6
// binary that emits its phase markers to stderr, wrapped exactly as real
// k6 0.54.0's logrus-based logger wraps console.log output (confirmed by
// actually running grafana/k6:0.54.0 against a trivial script during this
// investigation:
//
//	time="2026-08-08T11:43:02Z" level=info msg="TORTUREU_PHASE_START ramp_up 0" source=console
//
// not the bare `TORTUREU_PHASE_START ramp_up 0` k6.ParsePhaseMarker expects
// as fields[0]. An E1 eval against real k6 found this: real k6 writes
// console.log to stderr, not stdout, so scanMarkers (which read only
// stdout) never saw a marker, the scheduler's <-markers never yielded, and
// every phase-anchored fault silently never fired — this is the eighth
// instance in this build of something proven against a fake and never
// proven against the real thing.
func fakeK6ScriptStderrMarkers(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "k6")
	script := `#!/bin/sh
summary="$3"
echo 'time="2026-08-08T11:43:02Z" level=info msg="` + k6.PhaseMarkerPrefix + ` ramp_up 0" source=console' 1>&2
echo 'time="2026-08-08T11:43:03Z" level=info msg="` + k6.PhaseMarkerPrefix + ` peak 1000" source=console' 1>&2
echo '{"metrics":{"http_reqs":{"values":{"rate":10}}}}' > "$summary"
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// spec: R-EXE-8
//
// This is the regression test for the E1 finding above: real k6 writes
// phase markers to stderr, wrapped in its own logrus text-format line, not
// as a bare line on stdout. A fake that (like fakeK6Script above) writes
// bare lines to stdout will keep passing even if scanMarkers regresses
// back to stdout-only; this one only passes if stderr is actually scanned
// and k6's own log-line wrapping is actually unwrapped.
func TestK6Runner_EmitsPhaseMarkersFromRealK6sStderrLogFormat(t *testing.T) {
	dir := t.TempDir()
	r := K6Runner{Bin: fakeK6ScriptStderrMarkers(t), Dir: dir}

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
			t.Fatalf("timed out waiting for markers, got %v — stderr markers in k6's own log-wrapped format were not recognized", got)
		}
	}
	if got[0] != "ramp_up" || got[1] != "peak" {
		t.Errorf("markers = %v, want [ramp_up peak]", got)
	}
}

// spec: R-EXE-8
//
// The E1 finding was against real k6, and a fake proves only that the
// mechanism *can* work, not that it does against the actual binary this
// product ships against. This test runs the genuine grafana/k6 image (via
// K6Runner's container mode — the same docker-create/cp/start plumbing
// production uses once a SUT container is discovered) against a real
// one-line script, and proves handle.Markers() actually yields a marker
// k6 itself produced, not one this test's fixture manufactured.
func TestK6Runner_RealK6BinaryEmitsPhaseMarkers(t *testing.T) {
	dockerAvailable(t)
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH")
	}
	if err := exec.Command("docker", "image", "inspect", k6Image).Run(); err != nil {
		t.Skipf("real k6 image %s not available locally: %v", k6Image, err)
	}

	sut := startTestContainer(t)

	dir := t.TempDir()
	r := &K6Runner{Dir: dir}
	r.SetSUTContainer(sut)

	script := `export default function () {
  console.log("` + k6.PhaseMarkerPrefix + ` ramp_up " + Date.now());
}
`
	handle, err := r.Start(script)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	select {
	case m, ok := <-handle.Markers():
		if !ok {
			t.Fatal("markers channel closed with no marker — real k6's stderr output was not recognized")
		}
		if m.Phase != "ramp_up" {
			t.Errorf("marker phase = %q, want %q", m.Phase, "ramp_up")
		}
	case loadErr := <-handle.Err():
		t.Fatalf("Err() = %v, want a marker on Markers()", loadErr)
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for a real-k6-produced marker")
	}

	// Drain to let the container finish and clean up via Done()/Err(),
	// rather than leaving it running past the test.
	select {
	case <-handle.Done():
	case <-handle.Err():
	case <-time.After(30 * time.Second):
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
