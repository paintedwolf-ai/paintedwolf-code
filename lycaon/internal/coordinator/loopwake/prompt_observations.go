package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/actionoutcome"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
)

func (l *PromptObservations) ObservePrompt(ctx context.Context, sessionID string) func(inject.CoordinatorTurnFrame) {
	seq := l.Nudges.nextSequence()
	return func(frame inject.CoordinatorTurnFrame) {
		l.promptObservedSeq.Store(sessionID, seq)
		l.promptWorkflow.Store(sessionID, observedWorkflow{runID: frame.RunContext.RunID, revision: frame.WorkflowRevision, turn: actionoutcome.CurrentTurn(ctx)})
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
		value, _ := l.promptWorkflow.Load(sessionID)
		seen, _ := value.(observedWorkflow)
		pending.ticket.Consumed(seen.turn)
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
