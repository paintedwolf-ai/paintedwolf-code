package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"strings"
	"time"
)

// SetLoopWorkflowSource wires workflow run lookups for coordinator loop policy.
func (m *Manager) SetLoopWorkflowSource(src *loopwake.WorkflowDomains) {
	if m == nil {
		return
	}
	m.loopWorkflowSource = src
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

func (m *Manager) takeWorkerDigest(jobID string) string {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return ""
	}
	digest, ok := m.workerDigests.LoadAndDelete(jobID)
	if !ok {
		return ""
	}
	return strings.TrimSpace(digest)
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
	m.roundEndDrains.wait(ctx)
}

// parkBlockedLiveCommands waits for command completion after a repetition limit.
func (m *Manager) parkBlockedLiveCommands(ctx context.Context, sessionID string) bool {
	if m == nil || m.bgRegistry == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	jobs := m.bgRegistry.ActiveJobs(sessionID)
	if len(jobs) == 0 {
		return false
	}
	handles := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if handle := strings.TrimSpace(job.Handle); handle != "" {
			handles = append(handles, handle)
		}
	}
	if len(handles) == 0 {
		return false
	}
	loop := m.ensureCoordinatorRuntime().CoordinatorLoop()
	loop.EnterSleep(
		ctx,
		sessionID,
		time.Now().UTC().Add(time.Duration(loopwake.DefaultWaitSeconds)*time.Second),
		"waiting for active command after blocked turn",
		[]loopwake.WaitTrigger{loopwake.WaitTriggerTimer, loopwake.WaitTriggerProcessDone},
		handles,
		loopwake.SleepMoverHost,
	)
	loop.MarkWaitCalled(sessionID)
	// Completion between the job snapshot and EnterSleep needs an explicit wake.
	if !m.bgRegistry.HasRunningHandles(sessionID, handles) {
		loop.NudgeProcessFinished(ctx, sessionID, handles[0], anchor.Envelope{})
	}
	return true
}
