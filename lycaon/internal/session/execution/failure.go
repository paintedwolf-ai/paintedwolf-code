package execution

import (
	"context"
	"errors"
)

// TurnFailureSink reports a failed execution before queued follow-ups run.
type TurnFailureSink func(ctx context.Context, sessionID string, err error)

// SetFailureSink installs the reporter for failed executions.
func (m *Journal) SetFailureSink(sink TurnFailureSink) {
	if m == nil {
		return
	}
	m.turnFailure = sink
}

func (m *Journal) ReportFailure(ctx context.Context, sessionID string, err error) error {
	if m == nil || m.turnFailure == nil || err == nil {
		return err
	}
	m.turnFailure(context.WithoutCancel(ctx), sessionID, err)
	return reportedTurnFailure{err}
}

type reportedTurnFailure struct{ error }

func (e reportedTurnFailure) Unwrap() error { return e.error }

// UnreportedTurnFailure removes failures already delivered by the turn sink.
// Joined cleanup errors remain reportable after the execution error was delivered.
func UnreportedTurnFailure(err error) error {
	remaining, _ := unreportedTurnFailure(err)
	return remaining
}

func unreportedTurnFailure(err error) (error, bool) {
	if err == nil {
		return nil, false
	}
	if _, reported := err.(reportedTurnFailure); reported { //nolint:errorlint // Match this node only; joined siblings can still be unreported.
		return nil, true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		remaining := make([]error, 0, len(joined.Unwrap()))
		changed := false
		for _, child := range joined.Unwrap() {
			cause, removed := unreportedTurnFailure(child)
			remaining = append(remaining, cause)
			changed = changed || removed
		}
		if changed {
			return errors.Join(remaining...), true
		}
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		remaining, changed := unreportedTurnFailure(wrapped.Unwrap())
		if changed {
			return remaining, true
		}
	}
	return err, false
}
