package verdict

// confidenceRank orders the three confidence levels so one can be compared
// against a ceiling. A level this map does not know (including the empty
// string) has no rank, which AtMost treats as "no ceiling stated" rather
// than as the bottom of the scale — see AtMost.
var confidenceRank = map[Confidence]int{
	Ambiguous:  0,
	Correlated: 1,
	Caused:     2,
}

// AtMost clamps c to a ceiling, returning whichever is lower (R-VER-14).
//
// The ceiling is `observability.max_confidence`: what this repo's telemetry
// can support at all (R-DET-6, TBD-6). A finding may never claim more than
// it — a `caused` finding under a `correlated` ceiling would be the tool
// contradicting, in the same document, its own statement about what it can
// know. AtMost never *raises* c: a ceiling is permission, not evidence.
//
// An unrecognized or empty ceiling does not clamp. Detection never emits
// one (the floor is `correlated`), so the only way to reach that case is a
// caller that did not set the field, and silently downgrading a finding
// derived from real ingested spans because an unrelated field was left
// blank would discard evidence rather than protect anyone from it.
func (c Confidence) AtMost(ceiling Confidence) Confidence {
	ceilRank, ok := confidenceRank[ceiling]
	if !ok {
		return c
	}
	if confidenceRank[c] > ceilRank {
		return ceiling
	}
	return c
}
