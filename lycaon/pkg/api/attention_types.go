package api

// AttentionClass is what a session is asking of the human, worst first.
type AttentionClass string

const (
	// AttentionClassNeedsYou is a session parked on a human answer. It cannot
	// advance until someone responds. A worker request_decision escalation does
	// not qualify: the coordinator answers those, so they are work in flight.
	AttentionClassNeedsYou AttentionClass = "needs_you"
	// AttentionClassFinished is an idle session whose newest turn completed
	// after the person last read it. Reading the session clears it.
	AttentionClassFinished AttentionClass = "finished"
	// AttentionClassRunning is a turn in flight.
	AttentionClassRunning AttentionClass = "running"
	// AttentionClassError is a last turn that ended in host failure.
	AttentionClassError AttentionClass = "error"
)

// AttentionReason is the machine fact a class was derived from. Every value
// names an observable state, never a reading of message text.
type AttentionReason string

const (
	AttentionReasonCheckpoint   AttentionReason = "checkpoint"
	AttentionReasonAsk          AttentionReason = "ask"
	AttentionReasonTurnRunning  AttentionReason = "turn_running"
	AttentionReasonTurnFinished AttentionReason = "turn_finished"
	AttentionReasonTurnError    AttentionReason = "turn_error"
)

// AttentionRank orders classes for display. Lower sorts first.
//
// Errored and finished both have something to read; a running turn is still
// resolving itself.
func AttentionRank(c AttentionClass) int {
	switch c {
	case AttentionClassNeedsYou:
		return 0
	case AttentionClassError:
		return 1
	case AttentionClassFinished:
		return 2
	case AttentionClassRunning:
		return 3
	default:
		return 4
	}
}
