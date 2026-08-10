package main

import (
	"strings"
	"testing"
)

// spec: R-CLI-21
//
// Every published B1 fidelity number comes from Linux/cgroup v2. On macOS and
// Windows the faults land in a different kernel inside the Docker VM, so the
// magnitude of an injected fault is unverified there. "macOS is unmeasured"
// was true and written only in launch notes no user would read.
func TestFidelityNote_SaysMeasuredOnLinux(t *testing.T) {
	note := fidelityNote("linux")
	if !strings.Contains(strings.ToLower(note), "measured") {
		t.Errorf("linux note does not say fidelity is measured: %q", note)
	}
	if strings.Contains(strings.ToLower(note), "not been measured") {
		t.Errorf("linux note claims unmeasured: %q", note)
	}
}

// spec: R-CLI-21
func TestFidelityNote_FlagsUnmeasuredPlatforms(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		note := fidelityNote(goos)
		low := strings.ToLower(note)
		if !strings.Contains(low, "not been measured") {
			t.Errorf("%s: note does not flag fidelity as unmeasured: %q", goos, note)
		}
		// It must scope the caveat: orchestration is fine, magnitude is not.
		if !strings.Contains(low, "magnitude") {
			t.Errorf("%s: note does not say what is actually affected: %q", goos, note)
		}
	}
}

// spec: R-CLI-21
//
// And it must reach the report, not just exist as a function.
func TestBuildDoctorReport_CarriesTheFidelityNote(t *testing.T) {
	report := buildDoctorReport(nil, fixtureRegistry(), nil, nil)
	if !strings.Contains(report, "FAULT FIDELITY") {
		t.Errorf("doctor report has no fidelity section:\n%s", report)
	}
}

// spec: R-CLI-21
//
// The note is inserted after "fault magnitude has ", so both branches have to
// read as English there. The linux branch first shipped as "fault magnitude
// has measured on this platform".
func TestFidelityNote_ReadsAsASentence(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		full := "fault magnitude has " + fidelityNote(goos)
		if !strings.Contains(full, "has been measured") && !strings.Contains(full, "has NOT been measured") {
			t.Errorf("%s: reads as %q", goos, full[:60])
		}
	}
}
