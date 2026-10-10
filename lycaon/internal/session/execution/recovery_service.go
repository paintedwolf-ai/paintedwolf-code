package execution

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session/checkpointcontrol"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/pkg/api"
)

type RecoveryStore interface {
	Get(context.Context, string) (*api.Session, error)
	SetSessionStatus(context.Context, string, api.SessionStatus) error
	RecoverTurns(context.Context) ([]store.Turn, error)
	SettleAbandonedTurnClocks(context.Context) (int, error)
	RecoverTurnsForSession(context.Context, string) ([]store.Turn, error)
	ListBusySessionIDs(context.Context, int) ([]string, error)
}

// Recovery fences interrupted turns and restores missing tool projections.
type Recovery struct {
	store       RecoveryStore
	transcript  *transcript.Service
	status      *Status
	rewinds     *checkpointcontrol.Rewinds
	invocations invocation.Recorder
}

func NewRecovery(store RecoveryStore, transcript *transcript.Service, status *Status, rewinds *checkpointcontrol.Rewinds) *Recovery {
	return &Recovery{store: store, transcript: transcript, status: status, rewinds: rewinds}
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
