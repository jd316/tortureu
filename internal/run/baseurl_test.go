package run

import (
	"strings"
	"testing"

	"github.com/jd316/TortureU/internal/detect"
)

// spec: R-EXE-28
//
// k6 with no base URL requests "/" with no scheme, every request fails, and
// the run returns a verdict whose http_req_failed rate is 1.0 — a document
// that reads as a finding about the user's service and is entirely an
// artefact of a missing config field. Refuse before reset and before load.
func TestRun_RefusesAnEmptyBaseURL(t *testing.T) {
	cfg := minimalConfig()
	cfg.Target.BaseURL = ""

	reset := &fakeResetter{}
	v := Run(cfg, detect.System{}, Deps{
		Reset:    reset,
		Topology: &fakeTopology{},
		Load:     &fakeLoadRunner{handle: newFakeLoadHandle()},
		Applier:  &fakeApplier{},
	}, Options{})

	if v.Status != "error" {
		t.Fatalf("status = %q, want error — an empty base_url is a config fault, not a finding", v.Status)
	}
	if !strings.Contains(v.Error, "base_url") {
		t.Errorf("error = %q, want it to name target.base_url", v.Error)
	}
	if reset.called {
		t.Error("reset ran despite the refusal; R-EXE-28 requires refusing before reset")
	}
	if len(v.Findings) != 0 {
		t.Errorf("refusal produced findings %v, want none — nothing was measured", v.Findings)
	}
}
