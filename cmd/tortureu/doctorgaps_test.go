package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jd316/tortureu/internal/detect"
)

// spec: R-CLI-20
//
// doctor referenced System.Gaps nowhere, so every gap detection produced was
// invisible in the one verb whose job is saying what it could and could not
// determine. init printed them all.
func TestBuildDoctorReport_SurfacesDetectionGaps(t *testing.T) {
	sys := &detect.System{
		SUT:  "",
		Gaps: []string{"system under test not decided: 2 compose services declare build: (vote, worker)"},
	}
	report := buildDoctorReport(nil, fixtureRegistry(), sys, nil)
	if !strings.Contains(report, "system under test not decided") {
		t.Errorf("doctor hides detection's gaps:\n%s", report)
	}
}

// spec: R-CLI-20
//
// And with no gaps it must not invent an empty section.
func TestBuildDoctorReport_NoGapsSectionWhenThereAreNone(t *testing.T) {
	report := buildDoctorReport(nil, fixtureRegistry(), &detect.System{SUT: "api"}, nil)
	if strings.Contains(strings.ToUpper(report), "GAPS") {
		t.Errorf("empty gaps section rendered:\n%s", report)
	}
}

// spec: R-CLI-20
//
// -service must let a user resolve what R-DET-19 refused to guess, the same
// way init does; otherwise an ambiguous stack is a dead end at doctor.
func TestDoctor_ServiceFlagResolvesAnAmbiguousStack(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"vote", "worker"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const yml = `services:
  vote:
    build: ./vote
    ports: ["5000:80"]
  worker:
    build: ./worker
`
	compose := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(compose, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := runDoctor([]string{"-compose", compose, "-service", "vote"}, &out, &errb); code != 0 {
		t.Fatalf("doctor exit = %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "vote") {
		t.Errorf("-service did not reach the report:\n%s", out.String())
	}
	if strings.Contains(out.String(), "system under test not decided") {
		t.Errorf("-service was given but the ambiguity gap is still reported:\n%s", out.String())
	}
}
