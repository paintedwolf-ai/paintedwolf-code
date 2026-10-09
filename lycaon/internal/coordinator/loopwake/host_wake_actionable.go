package loopwake

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/pkg/api"
)

// HostWakeActionableFromHistory reports whether a host wake has new work for the LLM.
func HostWakeActionableFromHistory(
	history []api.Message,
	state surface.ImplementSessionState,
) bool {
	if state.BatchPhase == batch.PhaseClosed {
		return false
	}
	if len(state.PendingOverlayIDs) > 0 {
		return true
	}
	// needs_decision stays actionable while sibling workers are in flight.
	if historyHasNeedsDecision(history) {
		return true
	}
	if state.WorkersInFlight != 0 {
		return false
	}
	// A committed report settles this user-intent window until newer worker
	// evidence or another explicit machine obligation arrives.
	if intentHasSettledCloseout(history) {
		return false
	}
	return true
}

func historyHasNeedsDecision(history []api.Message) bool {
	for i := len(history) - 1; i >= 0; i-- {
		ws := history[i].WorkerSummary
		if ws == nil {
			continue
		}
		if ws.Status == api.WorkerSummaryStatusNeedsDecision {
			return true
		}
	}
	return false
}

// intentHasSettledCloseout reports a committed report or prose answer in the current
// user-intent window with no newer worker summary or tool step to reconcile.
func intentHasSettledCloseout(history []api.Message) bool {
	since := api.UserIntentBoundary(history)
	if since < 0 {
		since = 0
	}
	lastCloseout := -1
	lastWorker := -1
	for i := since; i < len(history); i++ {
		m := history[i]
		if m.Role == api.MessageRoleAssistant && m.Visibility == api.MessageVisibilityTranscript {
			switch {
			case isCloseoutAnswer(m):
				lastCloseout = i
			case len(m.ToolCalls) > 0:
				// A later tool step, such as ask_user, reopens the intent.
				lastCloseout = -1
			}
		}
		if m.WorkerSummary != nil {
			lastWorker = i
		}
	}
	return lastCloseout >= 0 && lastCloseout > lastWorker
}

// isCloseoutAnswer reports a completion report or a committed prose draft.
// Retried tool steps also commit as drafts; their tool calls await results.
func isCloseoutAnswer(m api.Message) bool {
	if len(m.ToolCalls) > 0 {
		return false
	}
	return m.Kind == api.MessageKindCompletionReport ||
		(m.Kind == api.MessageKindDraft && m.DraftStatus == api.DraftStatusCommitted)
}

// HostWakeActionableDeps loads transcript context for skip-turn policy.
type HostWakeActionableDeps struct {
	GetMessages           func(ctx context.Context, sessionID string) ([]api.Message, error)
	GetSession            func(ctx context.Context, sessionID string) (*api.Session, error)
	ImplementSessionState func(ctx context.Context, sess *api.Session) surface.ImplementSessionState
	ActiveRun             func(ctx context.Context, sessionID string) (*api.WorkflowRun, error)
	// WorkflowObligationsOpen reports unmet phase gates on the active running
	// workflow; open obligations keep every host wake actionable.
	WorkflowObligationsOpen func(ctx context.Context, sessionID string) bool
}

// BuildHostWakeActionable returns a LoopDeps.HostWakeActionable closure.
func BuildHostWakeActionable(deps HostWakeActionableDeps) func(ctx context.Context, in HostWakeActionableInput) bool {
	return func(ctx context.Context, in HostWakeActionableInput) bool {
		if deps.WorkflowObligationsOpen != nil && deps.WorkflowObligationsOpen(ctx, in.SessionID) {
			return true
		}
		if deps.GetMessages == nil {
			return true
		}
		msgs, err := deps.GetMessages(ctx, in.SessionID)
		if err != nil {
			return true
		}
		state := surface.ImplementSessionState{}
		if deps.GetSession != nil && deps.ImplementSessionState != nil {
			sess, sessErr := deps.GetSession(ctx, in.SessionID)
			if sessErr == nil && sess != nil {
				state = deps.ImplementSessionState(ctx, sess)
			}
		}
		if !HostWakeActionableFromHistory(msgs, state) {
			return false
		}
		if deps.ActiveRun == nil {
			return true
		}
		run, err := deps.ActiveRun(ctx, in.SessionID)
		if err != nil {
			return true
		}
		if run != nil && run.Status == api.WorkflowRunStatusRunning {
			return true
		}
		// Without a running workflow, only pending promotion or a worker decision is actionable.
		if len(state.PendingOverlayIDs) > 0 || historyHasNeedsDecision(msgs) {
			return true
		}
		return false
	}
}
