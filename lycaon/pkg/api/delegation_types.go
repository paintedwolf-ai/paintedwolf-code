package api

type LegStatus string

const (
	LegStatusPending      LegStatus = "pending"
	LegStatusDispatched   LegStatus = "dispatched"
	LegStatusRunning      LegStatus = "running"
	LegStatusRetryPending LegStatus = "retry_pending"
	LegStatusComplete     LegStatus = "complete"
	LegStatusFailed       LegStatus = "failed"
	LegStatusHeld         LegStatus = "held"
	LegStatusCanceled     LegStatus = "canceled"
)

// IsTerminal reports whether the leg will not run again.
func (s LegStatus) IsTerminal() bool {
	switch s {
	case LegStatusComplete, LegStatusFailed, LegStatusCanceled:
		return true
	case LegStatusPending, LegStatusDispatched, LegStatusRunning, LegStatusRetryPending, LegStatusHeld:
		return false
	}
	return false
}
