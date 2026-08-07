package egress

import (
	"fmt"
	"sort"
	"strings"
)

// UnclassifiedError is returned when one or more hosts reachable from the
// stack have no egress classification. Its Error() names every offending
// host, sorted for stable output — an abort that doesn't say which host is
// unactionable (R-DC2-2).
type UnclassifiedError struct {
	Hosts []string
}

func (e *UnclassifiedError) Error() string {
	return fmt.Sprintf("egress: unclassified host(s), run aborted before load: %s", strings.Join(e.Hosts, ", "))
}

// CheckUnclassified reports an *UnclassifiedError naming every host still
// classified ClassUnclassified. The caller (internal/run) MUST treat a
// non-nil result as a hard abort before load starts, exit code 3 (R-DC2-2,
// R-VER-7).
func CheckUnclassified(classes map[string]Class) error {
	var hosts []string
	for host, class := range classes {
		// R-DC2-6: an unrecognised class fails closed as unclassified,
		// independently of whether Classify or config.Parse already caught
		// it — this function does not trust either.
		if class == ClassUnclassified || !isKnownClass(class) {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	sort.Strings(hosts)
	return &UnclassifiedError{Hosts: hosts}
}
