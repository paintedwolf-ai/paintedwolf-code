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
		l.promptWorkflow.Store(sessionID, observedWorkflow{sequence: seq, runID: frame.RunContext.RunID, revision: frame.WorkflowRevision, turn: actionoutcome.CurrentTurn(ctx)})
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
	value, ok := l.promptWorkflow.Load(sessionID)
	if !ok {
		return false
	}
	seen := value.(observedWorkflow)
	consumed := pending.seq < seen.sequence
	if pending.wake == anchor.PhaseAdvanced && pending.runID != "" && pending.revision > 0 {
		consumed = seen.runID == pending.runID && seen.revision >= pending.revision
	}
	if consumed {
		pending.ticket.Consumed(seen.turn)
	}
	return consumed
}

type PromptObservations struct {
	promptWorkflow sync.Map
	Nudges         *Nudges
	Deliveries     *WaitDeliveries
	Subscriptions  *WaitSubscriptions
}
