package runstate

import (
	"errors"
	"fmt"
)

// PhaseGateUnmetError indicates advance was blocked by an unsatisfied phase gate.
type PhaseGateUnmetError struct {
	Phase        string
	Reason       string
	FailedGate   string
	FailedLeaves []string
	Replayed     bool
}

func (e *PhaseGateUnmetError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("phase gate unmet: %s", e.Reason)
	}
	return "phase gate unmet"
}

func IsPhaseGateUnmet(err error) (*PhaseGateUnmetError, bool) {
	var pe *PhaseGateUnmetError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}
