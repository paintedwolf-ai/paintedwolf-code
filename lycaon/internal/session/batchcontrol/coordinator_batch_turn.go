package batchcontrol

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
)

func (m *Service) BeginTurn(sessionID string) {
	if m == nil || sessionID == "" {
		return
	}
	m.turns.Delete(sessionID)
}

func (m *Service) TurnGuard(sessionID string) guard.BatchTurnGuard {
	if m == nil || sessionID == "" {
		return guard.BatchTurnGuard{}
	}
	if accepted, ok := m.turns.Load(sessionID); ok && accepted {
		return guard.BatchTurnGuard{SynthesisAcceptedThisTurn: true}
	}
	return guard.BatchTurnGuard{}
}

func (m *Service) AcceptSynthesis(ctx context.Context, sessionID string) {
	if m == nil || sessionID == "" {
		return
	}
	m.turns.Store(sessionID, true)
	if m.store == nil {
		return
	}
	sess, err := m.store.Get(ctx, sessionID)
	if err != nil || sess == nil || sess.IsWorkerChild() {
		return
	}
	state := m.state.ForSession(ctx, sess)
	seq := state.BatchSeq
	m.Apply(ctx, sessionID, batch.EventGroundedSynthesisAccepted, seq)
}
