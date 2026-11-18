package visual

// AbsenceReasons returns every reason.
func AbsenceReasons() []AbsenceReason {
	return []AbsenceReason{AbsenceUnknown, AbsenceForeign, AbsenceUnavailable, AbsenceDeleted}
}
