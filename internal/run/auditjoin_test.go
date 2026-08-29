package run

import (
	"testing"

	"github.com/jd316/TortureU/internal/config"
	"github.com/jd316/TortureU/internal/detect"
	"github.com/jd316/TortureU/internal/doctor"
)

// spec: R-VER-4
// spec: R-AUD-5
//
// TBD-10's resolution attributes a standard-library HTTP finding to the
// SUT service whose source contains it, naming its experiment target as
// undetermined rather than picking a dependency at random. buildCandidates
// then looked audit findings up by the FAULT TARGET's hostname only, so
// those two keys could never join and the knob never reached a verdict —
// `doctor` named Timeout on the same repo while `run` offered nothing.
//
// This is the E1 corpus's case 1 ("HTTP client with no timeout"), the
// canonical case, reproduced as a unit test.
func TestBuildCandidates_JoinsAuditFindingAttributedToTheSUT(t *testing.T) {
	fault := config.Fault{Name: "dep_slow", Target: "dep:9090", Inject: map[string]any{"latency": "300ms"}}
	// No Dep record: "dep" is a bare compose service with no recognized
	// image, exactly as in case 1.
	deps := []detect.Dep{}
	audit := []doctor.Finding{{
		DepName: "checkout-api", // the SUT, per TBD-10 — not "dep"
		Library: "net/http",
		Check:   doctor.CheckTimeout,
	}}

	got := buildCandidates(fault, deps, audit, "go", "checkout-api")

	if len(got) == 0 {
		t.Fatal("no candidates: the SUT-attributed audit finding did not join, so the verdict names no knob")
	}
	var libs []string
	for _, c := range got {
		libs = append(libs, c.Library)
	}
	found := false
	for _, c := range got {
		if c.Library == "net/http" {
			found = true
			if len(c.Knobs) == 0 {
				t.Errorf("net/http candidate carries no knobs; want at least Client.Timeout")
			}
		}
	}
	if !found {
		t.Fatalf("candidates = %v, want one for net/http", libs)
	}
}

// spec: R-AUD-5
//
// The join must stay keyed: a finding attributed to some OTHER service must
// not be offered as a candidate for this SUT's fault, or the verdict would
// suggest a knob in code that had nothing to do with the run.
func TestBuildCandidates_DoesNotJoinAnUnrelatedService(t *testing.T) {
	fault := config.Fault{Name: "dep_slow", Target: "dep:9090", Inject: map[string]any{"latency": "300ms"}}
	audit := []doctor.Finding{{DepName: "some-other-service", Library: "net/http", Check: doctor.CheckTimeout}}

	for _, c := range buildCandidates(fault, []detect.Dep{}, audit, "go", "checkout-api") {
		if c.Library == "net/http" {
			t.Fatalf("joined an audit finding attributed to %q while the SUT is checkout-api", audit[0].DepName)
		}
	}
}
