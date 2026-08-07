package egress

import "github.com/jdb316/tortureu/internal/verdict"

// Audit converts a resolved classification into the verdict's egress audit
// (R-VER-6): the auditable evidence that nothing escaped unmocked and
// unbounded. class: internal hosts are compose-graph dependencies, not
// egress in the DC-2 sense, and are intentionally excluded — the verdict
// schema (verdict.EgressAudit) has no bucket for them.
func Audit(classes map[string]Class) verdict.EgressAudit {
	var a verdict.EgressAudit
	for host, class := range classes {
		switch class {
		case ClassMock:
			a.Mocked = append(a.Mocked, host)
		case ClassBlock:
			a.Blocked = append(a.Blocked, host)
		case ClassReal:
			a.Real = append(a.Real, host)
		case ClassUnclassified:
			a.Unclassified = append(a.Unclassified, host)
		case ClassInternal:
			// intentionally excluded, see doc comment above.
		default:
			// R-DC2-6: a class this package does not recognise MUST NOT
			// fall through every case and land in no bucket at all — that
			// is exactly the "clean audit that isn't" DC-2 exists to
			// prevent. Fail closed into Unclassified.
			a.Unclassified = append(a.Unclassified, host)
		}
	}
	return a
}
