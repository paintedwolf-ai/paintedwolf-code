package execution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/transcript"
)

// Recovery fences interrupted turns and restores missing tool projections.
type Recovery struct {
	sessionStore Sessions
	turnStore    Turns
	transcript   *transcript.Service
	status       *Status
	rewinds      *checkpointcontrol.Rewinds
	invocations  invocation.Recorder
}

func NewRecovery(sessionStore Sessions, turnStore Turns, transcript *transcript.Service, status *Status, rewinds *checkpointcontrol.Rewinds) *Recovery {
	return &Recovery{sessionStore: sessionStore, transcript: transcript, status: status, rewinds: rewinds}
}
func (m *Recovery) SetRecorder(recorder invocation.Recorder) { m.invocations = recorder }
func (m *Recovery) InterruptTools(ctx context.Context, id string) error {
	interrupter, ok := m.invocations.(invocation.SessionProjectionRecoveryRecorder)
	if !ok {
		return nil
	}
	if _, err := interrupter.InterruptRunningSession(ctx, id); err != nil {
		return fmt.Errorf("interrupt tool invocations for session %s: %w", id, err)
	}
	if err := m.RecoverInterruptedToolResultPagesForSession(ctx, interrupter, id); err != nil {
		return fmt.Errorf("reconcile tool results for session %s: %w", id, err)
	}
	return nil
}
