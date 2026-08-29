// Package egress implements DC-2's default-deny egress guarantee: every
// host the system under test can reach is classified, and anything left
// unclassified aborts the run rather than being let through (R-DC2-1,
// R-DC2-2). See SPEC.md §2 and RESEARCH.md "DC-2" for the reasoning.
package egress

import "github.com/jd316/TortureU/internal/config"

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
// "internal" or "unclassified" — populated only for compose services
// matching detect's closed image vocabulary, so it is not a complete list
// of hosts. The result is the UNION of detected and cfg.Hosts: a host the
// user declared is the strongest possible signal of intent and must be
// classified even when detect never found it (R-DC2-1, R-DET-3/R-DET-7 — an
// unknown must surface, never vanish). Any host present in neither source
// stays out of the result; any host present but not resolvable to a known
// class stays unclassified — default-deny.
func Classify(detected map[string]string, cfg config.Egress) map[string]Class {
	classes := make(map[string]Class, len(detected)+len(cfg.Hosts))
	for host, dclass := range detected {
		classes[host] = classifyOne(host, dclass, cfg)
	}
	for host := range cfg.Hosts {
		if _, already := classes[host]; already {
			continue
		}
		classes[host] = classifyOne(host, "", cfg)
	}
	return classes
}

// classifyOne resolves a single host: a user classification in cfg.Hosts
// wins outright (R-DC2-6: fail closed on an unrecognised class value);
// otherwise dclass == "internal" (detect's compose-graph finding) applies;
// otherwise the host is unclassified.
func classifyOne(host, dclass string, cfg config.Egress) Class {
	if eh, ok := cfg.Hosts[host]; ok {
		class := Class(eh.Class)
		if !isKnownClass(class) {
			class = ClassUnclassified
		}
		return class
	}
	if dclass == string(ClassInternal) {
		return ClassInternal
	}
	return ClassUnclassified
}
