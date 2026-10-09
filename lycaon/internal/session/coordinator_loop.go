package session

import (
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

// NudgeCoordinatorLoopAfterWorkerJobTerminal queues worker.task.finished inform and
// a per-job coordinator wake (may run while sibling task() jobs are still in flight).

// ResetLoopBudget clears the per-run coordinator loop cycle counter.

// ShouldLoopWake exposes loop policy evaluation for tests.

// WaitForCoordinatorAsyncTurns drains host turns before session resources are released.

// parkBlockedLiveCommands waits for command completion after a repetition limit.
