package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
)

func (m *Manager) resetCoordinatorBatchTurnGuard(sessionID string) {
	if m == nil || sessionID == "" {
		return
	}
	m.coordinatorBatchTurn.Delete(sessionID)
}

func (m *Manager) coordinatorBatchTurnGuard(sessionID string) guard.BatchTurnGuard {
	if m == nil || sessionID == "" {
		return guard.BatchTurnGuard{}
	}
	if accepted, ok := m.coordinatorBatchTurn.Load(sessionID); ok && accepted {
		return guard.BatchTurnGuard{SynthesisAcceptedThisTurn: true}
	}
	return guard.BatchTurnGuard{}
}

func (m *Manager) acceptCoordinatorGroundedSynthesis(ctx context.Context, sessionID string) {
	if m == nil || sessionID == "" {
		return
	}
	m.coordinatorBatchTurn.Store(sessionID, true)
	if m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	state := m.BuildImplementSessionState(ctx, sess)
	seq := state.BatchSeq
	m.applyCoordinatorBatchEvent(ctx, sessionID, batch.EventGroundedSynthesisAccepted, seq)
}
