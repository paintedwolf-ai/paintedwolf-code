package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
)

// TryConsumeLoopBudgetForTest exposes budget consumption for unit tests.
func (m *Manager) TryConsumeLoopBudgetForTest(ctx context.Context, sessionID, runID string) bool {
	return m.ensureCoordinatorRuntime().CoordinatorLoop().Admission.ConsumeBudget(ctx, sessionID, runID, anchor.LegFinished)
}

// PendingKickIDForTest reports a queued kick without consuming it.
func (m *Manager) PendingKickIDForTest(sessionID string) (string, bool) {
	return m.ensureCoordinatorRuntime().Kicks().PeekPendingKickID(sessionID)
}

// ClearPendingKickForTest drops a queued coordinator kick.
func (m *Manager) ClearPendingKickForTest(sessionID string) {
	m.ensureCoordinatorRuntime().Kicks().ClearPending(sessionID)
}

// PendingLoopNudgeForTest reports a deferred loop nudge.
func (m *Manager) PendingLoopNudgeForTest(sessionID string) (anchor.ID, bool) {
	return m.ensureCoordinatorRuntime().CoordinatorLoop().Nudges.Pending(sessionID)
}

// DrainLoopPendingForTest runs the idle drain path for tests.
func (m *Manager) DrainLoopPendingForTest(ctx context.Context, sessionID string) {
	m.ensureCoordinatorRuntime().CoordinatorLoop().Nudges.DrainPending(ctx, sessionID)
}

// BeginPromptExecutionForTest marks the short-lived execution lane occupied.
func (m *Manager) BeginPromptExecutionForTest(ctx context.Context, sessionID string) func() {
	return m.ensureCoordinatorRuntime().CoordinatorLoop().Admission.BeginPromptExecution(ctx, sessionID)
}

// FinishPromptExecutionForTest settles an execution and drains queued wakes.
func (m *Manager) FinishPromptExecutionForTest(ctx context.Context, sessionID string, promptFailed bool, hostTurn bool) {
	_ = m.finishPromptExecution(ctx, sessionID, promptFailed, hostTurn, "")
	_ = m.drainPendingLoopWakes(ctx, sessionID)
}

// BeginPromptTurnForTest starts a prompt turn and clears the in-turn synthesis latch.
func (m *Manager) BeginPromptTurnForTest(sessionID, pendingKickID string) {
	m.beginPromptTurn(sessionID, pendingKickID)
}

// AcceptCoordinatorGroundedSynthesisForTest sets the in-turn latch and batch closed transition.
func (m *Manager) AcceptCoordinatorGroundedSynthesisForTest(ctx context.Context, sessionID string) {
	m.acceptCoordinatorGroundedSynthesis(ctx, sessionID)
}

// CoordinatorBatchTurnGuardForTest returns the in-turn batch guard snapshot.
func (m *Manager) CoordinatorBatchTurnGuardForTest(sessionID string) guard.BatchTurnGuard {
	return m.coordinatorBatchTurnGuard(sessionID)
}

// RejectFormatterForTest exposes the wired reject formatter for guard unit tests.
func (m *Manager) RejectFormatterForTest() *guidance.StaticRejectFormatter {
	if m == nil {
		return nil
	}
	return m.rejectFmt
}
