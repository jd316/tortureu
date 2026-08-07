// Package egress implements DC-2's default-deny egress guarantee: every
// host the system under test can reach is classified, and anything left
// unclassified aborts the run rather than being let through (R-DC2-1,
// R-DC2-2). See SPEC.md §2 and RESEARCH.md "DC-2" for the reasoning.
package egress

import "github.com/jdb316/tortureu/internal/config"

// Class is a host's resolved egress classification.
type Class string

const (
	ClassInternal     Class = "internal"     // in the compose graph (R-DET-4)
	ClassMock         Class = "mock"         // served by a local double
	ClassReal         Class = "real"         // allowed through, rate-limited (R-DC2-4)
	ClassBlock        Class = "block"        // silently dropped
	ClassUnclassified Class = "unclassified" // default-deny (R-DC2-1)
)

// Classify merges detect's static compose-graph classification with the
// user's torture.yaml egress policy into one final class per host
// (R-DC2-1, R-DET-4). detected is detect.System.EgressClass: host ->
// "internal" or "unclassified". Any host the user has not explicitly
// classified in cfg.Hosts, and that detect did not find in the compose
// graph, stays unclassified — default-deny.
func Classify(detected map[string]string, cfg config.Egress) map[string]Class {
	classes := make(map[string]Class, len(detected))
	for host, dclass := range detected {
		if eh, ok := cfg.Hosts[host]; ok {
			// R-DC2-6: fail closed rather than trust config.Parse already
			// rejected an unrecognised class string.
			class := Class(eh.Class)
			if !isKnownClass(class) {
				class = ClassUnclassified
			}
			classes[host] = class
			continue
		}
		if dclass == string(ClassInternal) {
			classes[host] = ClassInternal
			continue
		}
		classes[host] = ClassUnclassified
	}
	return classes
}
