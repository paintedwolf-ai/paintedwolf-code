package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"log/slog"
	"sync"
)

func (l *PromptObservations) ObservePrompt(sessionID string) func(inject.CoordinatorTurnFrame) {
	seq := l.Nudges.nextSequence()
	return func(frame inject.CoordinatorTurnFrame) {
		l.promptObservedSeq.Store(sessionID, seq)
		l.promptWorkflow.Store(sessionID, observedWorkflow{frame.RunContext.RunID, frame.WorkflowRevision})
		slog.Debug("prompt wake observation", "component", loopLogComponent, "session_id", sessionID, "sequence", seq, "run_id", frame.RunContext.RunID, "workflow_revision", frame.WorkflowRevision)
	}
}
func (l *PromptObservations) wakeConsumed(ctx context.Context, sessionID string, pending pendingLoopWake) bool {
	if _, ready := l.Deliveries.waitWinner(sessionID); ready {
		return false
	}
	// Subscribed waits require result delivery.
	if l.Subscriptions.matchesActiveWait(ctx, sessionID, pending) {
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
	if l.Nudges.kickStillQueued(sessionID, inform) {
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

type PromptObservations struct {
	promptObservedSeq sync.Map
	promptWorkflow    sync.Map
	Nudges            *Nudges
	Deliveries        *WaitDeliveries
	Subscriptions     *WaitSubscriptions
}

func (l *PromptObservations) lastPromptObservedSeq(sessionID string) uint64 {
	if v, ok := l.promptObservedSeq.Load(sessionID); ok {
		seq, _ := v.(uint64)
		return seq
	}
	return 0
}
