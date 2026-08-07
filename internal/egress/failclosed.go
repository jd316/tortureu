package egress

// isKnownClass reports whether c is one of the five classes this package
// defines. Used by Classify, CheckUnclassified, and Audit independently, so
// each fails closed on its own rather than trusting that some other
// function (or config.Parse) already rejected the value (R-DC2-6).
func isKnownClass(c Class) bool {
	switch c {
	case ClassInternal, ClassMock, ClassReal, ClassBlock, ClassUnclassified:
		return true
	default:
		return false
	}
}
