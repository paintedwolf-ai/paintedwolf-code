package promptloop

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/pkg/api"
)

// finalTurnGaveNoAnswer reports a final turn the model spent on a call the host will
// not run, or on nothing at all. Earlier in a turn an empty reply stays a provider
// failure, which the run reports and the user can retry.
func finalTurnGaveNoAnswer(err error, st *promptLoopTurnState) bool {
	var toolOnly *ProseTurnToolCallError
	var empty *failure.ProviderEmptyCompletionError
	return errors.As(err, &toolOnly) || (st != nil && st.proseFinish && errors.As(err, &empty))
}

func (l *turnCloseout) canCloseWithoutAnswer(sess *api.Session) bool {
	return l != nil && l.Deps.AssembleLedgerCloseout != nil && sess != nil && !sess.IsWorkerChild()
}

// applyAssembledFinalTurn ends a final turn a finish guard refused. The host stores
// the answer it has, and says the host assembled it.
func (l *turnCloseout) applyAssembledFinalTurn(
	ctx context.Context,
	sess *api.Session,
	sessionID, userPrompt, surfaceID string,
	st *promptLoopTurnState,
	draft guidance.CloseoutDraft,
	content string,
	refusal *guidance.Refusal,
) (promptLoopAssistantTurnResult, error) {
	// A surface without a typed read still keeps its text for assembly.
	if !draft.HasAnswer() {
		draft = guidance.ParseCloseoutDraft(content, citationmd.Options{})
	}
	if !draft.HasAnswer() {
		return l.endWithClosing(ctx, sess, sessionID, userPrompt, surfaceID, st)
	}
	forcedBy := st.closeoutRetry.codes
	if code := refusal.Code(); code != "" {
		forcedBy = append(append([]string(nil), forcedBy...), code)
	}
	emitted, err := l.emitAssembledCloseout(ctx, sess, sessionID, userPrompt, surfaceID, draft, forcedBy, st, st.history)
	if err != nil {
		return promptLoopAssistantTurnResult{}, err
	}
	if !emitted.committed {
		return l.endWithClosing(ctx, sess, sessionID, userPrompt, surfaceID, st)
	}
	st.history = emitted.history
	st.proseFinishDelivered = true
	return promptLoopAssistantTurnResult{breakLoop: true, exit: loopExitCompleted}, nil
}

// endWithClosing ends a turn that produced no answer, so that the user is never left
// with nothing: the retained draft if there is one, or else a closing that says the
// answer was not written and what the run had reached.
func (l *turnCloseout) endWithClosing(
	ctx context.Context,
	sess *api.Session,
	sessionID, userPrompt, surfaceID string,
	st *promptLoopTurnState,
) (promptLoopAssistantTurnResult, error) {
	out := promptLoopAssistantTurnResult{breakLoop: true, exit: loopExitCompleted}
	forcedBy := append(append([]string(nil), st.closeoutRetry.codes...), guard.CoordinatorCloseoutNoAnswerCode)
	var drafts []guidance.CloseoutDraft
	if l.Deps.CloseoutStallState != nil {
		if retained := l.Deps.CloseoutStallState(ctx, sessionID); retained.Active && retained.Draft.HasAnswer() {
			drafts = append(drafts, retained.Draft)
		}
	}
	drafts = append(drafts, l.hostClosingDraft(ctx, sessionID, st))
	for _, draft := range drafts {
		emitted, err := l.emitAssembledCloseout(ctx, sess, sessionID, userPrompt, surfaceID, draft, forcedBy, st, st.history)
		if err != nil {
			return out, err
		}
		if emitted.committed {
			st.history = emitted.history
			st.proseFinishDelivered = true
			return out, nil
		}
	}
	st.turnEndedGuidanceReject = true
	return out, l.Nudges.closeCoordinatorDraftSlot(ctx, sessionID, st, st.draftSlotID)
}

func (l *turnCloseout) hostClosingDraft(ctx context.Context, sessionID string, st *promptLoopTurnState) guidance.CloseoutDraft {
	cause := st.closeoutCauseTextOrEmpty()
	if cause == "" {
		cause = TurnCloseoutReasonText(TurnCloseoutIterationCap)
	}
	// A closing still states the cause when the evidence cannot be read.
	ev, _ := guidance.UnionCloseoutEvidence(ctx, l.Deps.EvidenceLedger, sessionID, st.history)
	return guidance.ParseCloseoutDraft(guidance.HostClosingMarkdown(cause, ev.Ledger, latestProgress(st.history)), citationmd.Options{})
}

// latestProgress is the checklist the model last set.
func latestProgress(history []api.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		for j := len(history[i].ToolCalls) - 1; j >= 0; j-- {
			call := history[i].ToolCalls[j]
			if call.Name != "update_progress" {
				continue
			}
			if content, ok := call.Args["content"].(string); ok && strings.TrimSpace(content) != "" {
				return strings.TrimSpace(content)
			}
		}
	}
	return ""
}
