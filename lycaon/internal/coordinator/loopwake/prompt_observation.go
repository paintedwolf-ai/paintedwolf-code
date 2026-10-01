package loopwake

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
)

type observedWorkflow struct {
	runID    string
	revision int64
}

// ObservePrompt captures wake order before context reads. Its callback records
// a successful response before tool execution.
func (l *LoopEngine) ObservePrompt(sessionID string) func(inject.CoordinatorTurnFrame) {
	seq := l.nudgeSeq.Add(1)
	return func(frame inject.CoordinatorTurnFrame) {
		l.promptObservedSeq.Store(sessionID, seq)
		l.promptWorkflow.Store(sessionID, observedWorkflow{frame.RunContext.RunID, frame.WorkflowRevision})
		slog.Debug("prompt wake observation", "component", loopLogComponent, "session_id", sessionID, "sequence", seq, "run_id", frame.RunContext.RunID, "workflow_revision", frame.WorkflowRevision)
	}
}

func (l *LoopEngine) wakeConsumed(ctx context.Context, sessionID string, pending pendingLoopWake) bool {
	if _, ready := l.waitWinner(sessionID); ready {
		return false
	}
	// Subscribed waits require result delivery.
	if l.matchesActiveWait(ctx, sessionID, pending) {
		return false
	}
	// Deferred guidance remains pending until queued.
	if pending.inform != "" && !pending.informHandled {
		return false
	}
	// Board facts do not replace queued guidance.
	inform := pending.inform
	if inform == "" {
		inform = pending.wake
	}
	if l.kickStillQueued(sessionID, inform) {
		return false
	}
	consumed := pending.seq < l.lastPromptObservedSeq(sessionID)
	if pending.wake == anchor.PhaseAdvanced && pending.runID != "" && pending.revision > 0 {
		// Phase events require an observed revision from the same run.
		consumed = false
		if value, ok := l.promptWorkflow.Load(sessionID); ok {
			observed := value.(observedWorkflow)
			consumed = observed.runID == pending.runID && observed.revision >= pending.revision
		}
	}
	if consumed {
		loopLogNudge(sessionID, pending.wake, pending.inform, pending.legID, pending.completingJobID, "consumed_by_prompt")
	}
	return consumed
}
