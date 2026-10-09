package session

import (
	"context"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
)

// SetLoopWorkflowSource wires workflow run lookups for coordinator loop policy.
func (m *Manager) SetLoopWorkflowSource(src *loopwake.WorkflowDomains) {
	if m == nil {
		return
	}
	m.loopWorkflowSource = src
	m.Runner.Settlement.SetWorkflowSource(src)
}

// NudgeCoordinatorLoop queues an optional inform and maybe runs a host coordinator turn.
func (m *Manager) NudgeCoordinatorLoop(ctx context.Context, sessionID string, wake, inform anchor.ID, legID string, env anchor.Envelope) {
	m.ensureCoordinatorRuntime().CoordinatorLoop().Nudge(ctx, sessionID, wake, inform, legID, env)
}

// NudgeCoordinatorLoopAfterWorkerJobTerminal queues worker.task.finished inform and
// a per-job coordinator wake (may run while sibling task() jobs are still in flight).
func (m *Manager) NudgeCoordinatorLoopAfterWorkerJobTerminal(
	ctx context.Context,
	parentID, completingJobID string,
	env anchor.Envelope,
) {
	if m == nil || strings.TrimSpace(parentID) == "" || strings.TrimSpace(completingJobID) == "" {
		return
	}
	m.ensureCoordinatorRuntime().CoordinatorLoop().NudgeAfterWorkerJobTerminal(
		ctx,
		parentID,
		completingJobID,
		anchor.WorkerTaskFinished,
		anchor.WorkerTaskFinished,
		"",
		env,
	)
}

// ResetLoopBudget clears the per-run coordinator loop cycle counter.
func (m *Manager) ResetLoopBudget(runID string) {
	m.ensureCoordinatorRuntime().CoordinatorLoop().ResetBudget(runID)
}

func (m *Manager) nudgeLegFinished(ctx context.Context, parentID string, completedAt time.Time, legID string) {
	m.ensureCoordinatorRuntime().CoordinatorLoop().NudgeLegFinished(ctx, parentID, completedAt, legID)
}

// ShouldLoopWake exposes loop policy evaluation for tests.
func (m *Manager) ShouldLoopWake(ctx context.Context, sessionID string, wake anchor.ID) (bool, string, error) {
	return m.ensureCoordinatorRuntime().CoordinatorLoop().ShouldLoopWake(ctx, sessionID, wake)
}

// WaitForCoordinatorAsyncTurns drains host turns before session resources are released.
func (m *Manager) WaitForCoordinatorAsyncTurns(ctx context.Context) {
	if m == nil {
		return
	}
	m.ensureCoordinatorRuntime().CoordinatorLoop().WaitForAsyncTurns(ctx)
	m.Admission.WaitDrains(ctx)
}

// parkBlockedLiveCommands waits for command completion after a repetition limit.
