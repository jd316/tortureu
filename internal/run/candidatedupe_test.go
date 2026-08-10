package run

import (
	"testing"

	"github.com/jdb316/tortureu/internal/config"
	"github.com/jdb316/tortureu/internal/doctor"
	"github.com/jdb316/tortureu/internal/verdict"
)

// spec: R-VER-4
//
// attribute() calls buildCandidates once per active fault and each call
// dedupes only within itself, so a client reachable from two faults was
// listed twice — verbatim. Seen for real on the multi-fault corpus case:
// net/http and its five knobs printed twice in the "look at:" block, which
// is the line a user is meant to act on.
func TestAttribute_DoesNotRepeatACandidateAcrossFaults(t *testing.T) {
	f := &verdict.Finding{}
	faults := []config.Fault{
		{Name: "a", Target: "dep-a:9091", Inject: map[string]any{"latency": "3s"}},
		{Name: "b", Target: "dep-b:9092", Inject: map[string]any{"down": true}},
	}
	audit := []doctor.Finding{{DepName: "checkout-api", Library: "net/http", Check: doctor.CheckTimeout}}

	attribute(f, faults, nil, audit, "go", "checkout-api")

	seen := map[string]int{}
	for _, c := range f.Candidates {
		seen[c.Library]++
	}
	for lib, n := range seen {
		if n > 1 {
			t.Errorf("candidate %q listed %d times; a user reads this list as distinct things to try", lib, n)
		}
	}
}
