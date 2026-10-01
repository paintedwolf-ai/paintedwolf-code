package promptloop

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// SpendRunway reports proximity to the session spend ceiling.
type SpendRunway struct {
	Low        bool
	CeilingUSD float64
}

// SpendCeilingCheck reports a warning or permission for one final tool round.
type SpendCeilingCheck struct {
	Runway   SpendRunway
	SoftStop bool
}

// SpendRunwayNudge renders the first spend warning.
type SpendRunwayNudge func(ctx context.Context, sess *api.Session, ceilingUSD float64) HostNudge

// SpendSoftStopNudge renders instructions for the final tool round.
type SpendSoftStopNudge func(ctx context.Context, sess *api.Session) HostNudge

// spendCeilingDecision selects a warning, final tool round, or completed closeout.
type spendCeilingDecision struct {
	Runway   SpendRunway
	WindDown bool
	Finished *PromptRunResult
}

// A spent session may receive one final tool round before closeout.
func (l *PromptLoop) applySpendCeiling(
	ctx context.Context,
	sess *api.Session,
	sessionID, profileID, userPrompt string,
	maxIter int,
	in PromptRunInput,
	st *promptLoopTurnState,
) (spendCeilingDecision, error) {
	var out spendCeilingDecision
	if l == nil || l.Deps.CheckSpendCeiling == nil {
		return out, nil
	}
	check, err := l.Deps.CheckSpendCeiling(ctx, sessionID, sess)
	if err == nil {
		out.Runway = check.Runway
		return out, nil
	}
	if !l.spendCeilingReached(err) || strings.TrimSpace(st.lastAssistantID) == "" {
		return out, err
	}
	if sess != nil && !sess.IsWorkerChild() && !st.spendSoftStopGranted && check.SoftStop {
		st.spendSoftStopGranted = true
		out.WindDown = true
		return out, nil
	}
	closedHistory, aid, content, cerr := l.runEarlyTurnCloseout(
		ctx, sess, sessionID, profileID, userPrompt, st.history, maxIter,
		TurnCloseoutSpendCeiling, "", false, in, st,
	)
	if cerr != nil {
		return out, cerr
	}
	if aid == "" {
		return out, err
	}
	st.history = closedHistory
	out.Finished = &PromptRunResult{
		LastAssistantID:      aid,
		LastOutputID:         st.lastOutputID,
		LastAssistantContent: content,
		TurnTools:            st.turnTools,
		TasksDispatchedCount: st.tasksDispatchedCount,
	}
	return out, nil
}

func (l *PromptLoop) maybeSpendSoftStopNudge(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	windDown bool,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	if l == nil || l.Deps.SpendSoftStopNudge == nil || sess == nil || !windDown {
		return history, nil
	}
	nudge := l.Deps.SpendSoftStopNudge(ctx, sess)
	if nudge.Empty() {
		return history, nil
	}
	return l.appendHostNudge(ctx, sessionID, history, nudge, "", st)
}

func filterSpendSoftStopToolCalls(calls []api.ToolCall) []api.ToolCall {
	if len(calls) == 0 {
		return calls
	}
	out := make([]api.ToolCall, 0, len(calls))
	for _, call := range calls {
		if isTaskToolName(call.Name) {
			continue
		}
		out = append(out, call)
	}
	return out
}

func filterSpendSoftStopToolMetas(metas []tools.ToolMeta) []tools.ToolMeta {
	if len(metas) == 0 {
		return metas
	}
	out := make([]tools.ToolMeta, 0, len(metas))
	for _, meta := range metas {
		if isTaskToolName(meta.Name) {
			continue
		}
		out = append(out, meta)
	}
	return out
}

func (l *PromptLoop) maybeSpendRunwayNudge(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	history []api.Message,
	runway SpendRunway,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	if l == nil || l.Deps.SpendRunwayNudge == nil || sess == nil {
		return history, nil
	}
	if !runway.Low {
		return history, nil
	}
	nudge := l.Deps.SpendRunwayNudge(ctx, sess, runway.CeilingUSD)
	if nudge.Empty() {
		return history, nil
	}
	return l.appendHostNudge(ctx, sessionID, history, nudge, "", st)
}

func (l *PromptLoop) spendCeilingReached(err error) bool {
	if err == nil {
		return false
	}
	if l != nil && l.Deps.IsSpendCeiling != nil {
		return l.Deps.IsSpendCeiling(err)
	}
	return false
}
