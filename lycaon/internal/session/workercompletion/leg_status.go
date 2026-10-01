package workercompletion

// A leg status is the worker's own verdict on its assignment, distinct from
// api.LegStatus, the host's record of the leg's execution. The set is closed;
// codegen-host-markers projects it into Den.
const (
	// LegStatusComplete: the assignment is done.
	LegStatusComplete = "complete"
	// LegStatusPartial: some of the assignment landed; the rest is still open.
	LegStatusPartial = "partial"
	// LegStatusBlocked: the worker could not proceed and needs a decision.
	LegStatusBlocked = "blocked"
)

// legStatuses is the closed set, in escalating order of "needs attention".
var legStatuses = []string{LegStatusComplete, LegStatusPartial, LegStatusBlocked}

// LegStatuses returns the closed set as a copy, so no caller reorders it.
func LegStatuses() []string {
	return append([]string(nil), legStatuses...)
}
