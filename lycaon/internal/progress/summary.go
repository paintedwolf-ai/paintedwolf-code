package progress

// Summary separates terminal descope decisions from completed work.
type Summary struct {
	Pending                   int  `json:"pending"`
	Completed                 int  `json:"completed"`
	NotPursued                int  `json:"not_pursued"`
	Optional                  int  `json:"optional"`
	AllRequiredItemsTerminal  bool `json:"all_required_items_terminal"`
	AllRequiredItemsCompleted bool `json:"all_required_items_completed"`
}

// Summarize tallies rows by state.
func Summarize(rows []Row) Summary {
	var summary Summary
	for _, r := range rows {
		switch r.State {
		case ProgressStatePending:
			summary.Pending++
		case ProgressStateDone:
			summary.Completed++
		case ProgressStateNA:
			summary.NotPursued++
		case ProgressStateOptional:
			summary.Optional++
		}
	}
	summary.AllRequiredItemsTerminal = len(rows) > 0 && summary.Pending == 0
	summary.AllRequiredItemsCompleted = summary.Completed > 0 && summary.Pending == 0 && summary.NotPursued == 0
	return summary
}

// ProgressMissing reports whether no progress rows exist for the current request.
func ProgressMissing(rows []Row) bool {
	return len(rows) == 0
}

// CloseCounts tallies completed, pending, and not-pursued rows.
func CloseCounts(rows []Row) (done, pending, na int) {
	s := Summarize(rows)
	return s.Completed, s.Pending, s.NotPursued
}

// OpenStepCount returns remaining blocking pending rows.
func OpenStepCount(rows []Row) int {
	return Summarize(rows).Pending
}

// HasOpenSteps reports whether any blocking pending row remains.
func HasOpenSteps(rows []Row) bool {
	return OpenStepCount(rows) > 0
}

// AllTerminal reports whether at least one row exists and none are pending.
func AllTerminal(rows []Row) bool {
	return Summarize(rows).AllRequiredItemsTerminal
}

// FirstPendingAfterOptional returns the first pending row label placed after an optional row.
func FirstPendingAfterOptional(rows []Row) (label string, found bool) {
	sawOptional := false
	for _, r := range rows {
		if r.State == ProgressStateOptional {
			sawOptional = true
			continue
		}
		if sawOptional && r.State == ProgressStatePending {
			return r.Label, true
		}
	}
	return "", false
}
