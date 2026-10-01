package promptloop

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/pkg/api"
)

var ErrLLMTurnTimeout = errors.New("llm turn timed out")

// TurnCloseoutReason classifies why the host is offering a final prose turn.
type TurnCloseoutReason string

const (
	TurnCloseoutIterationCap TurnCloseoutReason = "iteration_cap"
	TurnCloseoutSpendCeiling TurnCloseoutReason = "session_spend_ceiling"
	TurnCloseoutCanceled     TurnCloseoutReason = "canceled"
	TurnCloseoutStopped      TurnCloseoutReason = "stopped"
	TurnCloseoutLLMTimeout   TurnCloseoutReason = "llm_timeout"
	TurnCloseoutBlockedLoop  TurnCloseoutReason = "blocked_loop"
)

// BlockedLoopRejectCap counts repeated rejection codes within one streak.
const BlockedLoopRejectCap = 6

// BlockedLoopAbsoluteCap also bounds streaks of distinct rejection codes.
const BlockedLoopAbsoluteCap = 12

func schemaRejectExemptFromBlockedLoop(code string) bool {
	code = strings.TrimSpace(code)
	if tools.IsNativeCommandRedirectCode(code) {
		return true
	}
	switch code {
	case "TOOL_ARGS_INVALID", "TOOL_ARGS_MALFORMED", "TOOL_ARGS_TRUNCATED":
		return true
	default:
		return false
	}
}

func TurnCloseoutReasonText(reason TurnCloseoutReason) string {
	switch reason {
	case TurnCloseoutSpendCeiling:
		return "the session spend ceiling was reached"
	case TurnCloseoutCanceled:
		return "this run was canceled"
	case TurnCloseoutStopped:
		return "this run was stopped"
	case TurnCloseoutLLMTimeout:
		return "the LLM turn time limit was reached"
	case TurnCloseoutBlockedLoop:
		return "the same tool call was blocked too many times in a row"
	case TurnCloseoutIterationCap:
		fallthrough
	default:
		return "the per-turn tool-iteration limit was reached"
	}
}

// HostNudge carries rendered guidance and its anchor ID.
type HostNudge struct {
	Content  string
	SignalID string
	Feedback *api.ToolFeedback
}

func (n HostNudge) Empty() bool { return strings.TrimSpace(n.Content) == "" }

// TurnCloseoutNudge renders the internal user nudge for a prose-only finish turn.
type TurnCloseoutNudge func(ctx context.Context, sess *api.Session, profileID string, reason TurnCloseoutReason, cancelReason string) HostNudge

// IterationRunwayNudge renders the one-shot heads-up when few tool iterations remain.
type IterationRunwayNudge func(ctx context.Context, sess *api.Session, profileID string, remaining int) HostNudge

// IterationRunwayThreshold reserves final iterations for handoff on longer
// coordinator turns; a worker's runway follows spawn.WorkerRunway.
const IterationRunwayThreshold = 10

func (l *PromptLoop) maybeTurnCloseout(
	ctx context.Context,
	sess *api.Session,
	sessionID, profileID string,
	history []api.Message,
	iterIndex, maxIter int,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	if !guard.ProseFinishTurn(sess, iterIndex, maxIter) {
		return history, nil
	}
	return l.appendTurnCloseoutNudge(ctx, sess, sessionID, profileID, history, TurnCloseoutIterationCap, "", st)
}

// iterationRunwayFires includes the current zero-based iteration in the remaining budget.
func iterationRunwayFires(sess *api.Session, iterIndex, maxIter int) bool {
	if sess.IsWorkerChild() {
		runway := spawn.WorkerRunway(maxIter)
		return runway > 0 && maxIter-iterIndex == runway
	}
	if maxIter < 2*IterationRunwayThreshold {
		return false
	}
	return maxIter-iterIndex == IterationRunwayThreshold
}

func (l *PromptLoop) maybeIterationRunwayNudge(
	ctx context.Context,
	sess *api.Session,
	sessionID, profileID string,
	history []api.Message,
	iterIndex, maxIter int,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	if l == nil || l.Deps.IterationRunwayNudge == nil || sess == nil {
		return history, nil
	}
	if !iterationRunwayFires(sess, iterIndex, maxIter) {
		return history, nil
	}
	nudge := l.Deps.IterationRunwayNudge(ctx, sess, profileID, maxIter-iterIndex)
	if nudge.Empty() {
		return history, nil
	}
	return l.appendHostNudge(ctx, sessionID, history, nudge, "", st)
}

// applyLandingAdvisories appends closeout and runway guidance in order.
func (l *PromptLoop) applyLandingAdvisories(
	ctx context.Context,
	sess *api.Session,
	sessionID, profileID string,
	step, maxIter int,
	spendRunway SpendRunway,
	spendWindDown bool,
	st *promptLoopTurnState,
) (closeoutNudgeSent bool, err error) {
	updated, err := l.maybeTurnCloseout(ctx, sess, sessionID, profileID, st.history, step, maxIter, st)
	if err != nil {
		return false, err
	}
	if len(updated) > len(st.history) {
		closeoutNudgeSent = true
	}
	st.history = updated

	st.history, err = l.maybeWorkerBudgetAnswerNudge(ctx, sess, sessionID, st.history, step, maxIter, st)
	if err != nil {
		return closeoutNudgeSent, err
	}
	st.history, err = l.maybeIterationRunwayNudge(ctx, sess, sessionID, profileID, st.history, step, maxIter, st)
	if err != nil {
		return closeoutNudgeSent, err
	}
	st.history, err = l.maybeSurveyStreakNudge(ctx, sess, sessionID, st.history, st)
	if err != nil {
		return closeoutNudgeSent, err
	}
	st.history, err = l.maybeSpendRunwayNudge(ctx, sess, sessionID, st.history, spendRunway, st)
	if err != nil {
		return closeoutNudgeSent, err
	}
	st.history, err = l.maybeSpendSoftStopNudge(ctx, sess, sessionID, st.history, spendWindDown, st)
	return closeoutNudgeSent, err
}

func (l *PromptLoop) appendTurnCloseoutNudge(
	ctx context.Context,
	sess *api.Session,
	sessionID, profileID string,
	history []api.Message,
	reason TurnCloseoutReason,
	cancelReason string,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	nudge := l.turnCloseoutNudge(ctx, sess, profileID, reason, cancelReason)
	if nudge.Empty() {
		return history, nil
	}
	if historyHasInternalNudge(history, nudge.Content) {
		return history, nil
	}
	kind := api.MessageKind("")
	if reason == TurnCloseoutIterationCap {
		kind = api.MessageKindIterationCapCloseout
	}
	return l.appendHostNudge(ctx, sessionID, history, nudge, kind, st)
}

func historyHasInternalNudge(history []api.Message, content string) bool {
	want := strings.TrimSpace(content)
	for _, msg := range history {
		if msg.Role != api.MessageRoleUser || msg.Visibility != api.MessageVisibilityInternal {
			continue
		}
		if strings.TrimSpace(msg.Content) == want {
			return true
		}
	}
	return false
}

func (l *PromptLoop) turnCloseoutNudge(
	ctx context.Context,
	sess *api.Session,
	profileID string,
	reason TurnCloseoutReason,
	cancelReason string,
) HostNudge {
	if l == nil || l.Deps.TurnCloseoutNudge == nil {
		return HostNudge{}
	}
	nudge := l.Deps.TurnCloseoutNudge(ctx, sess, profileID, reason, cancelReason)
	nudge.Content = strings.TrimSpace(nudge.Content)
	return nudge
}

func (l *PromptLoop) runEarlyTurnCloseout(
	ctx context.Context,
	sess *api.Session,
	sessionID, profileID, userPrompt string,
	history []api.Message,
	maxIter int,
	reason TurnCloseoutReason,
	cancelReason string,
	skipNudge bool,
	in PromptRunInput,
	st *promptLoopTurnState,
) ([]api.Message, string, string, error) {
	if sess == nil || maxIter <= 0 {
		return history, "", "", nil
	}
	var err error
	if !skipNudge {
		history, err = l.appendTurnCloseoutNudge(ctx, sess, sessionID, profileID, history, reason, cancelReason, st)
		if err != nil {
			return history, "", "", err
		}
	}
	closeoutState := st
	if closeoutState == nil {
		closeoutState = &promptLoopTurnState{}
	}
	closeoutState.proseFinish = true
	iterIndex := closeoutState.turnsRanThisRun
	assistantMsg, completion, _, err := l.runAssistantStreamTurn(ctx, sessionID, sess, history, closeoutState, profileID, userPrompt, iterIndex, maxIter, in.HostTurn)
	if err != nil {
		return history, "", "", err
	}
	if st != nil {
		st.proseFinishDelivered = true
	}
	surfaceID := l.promptTurnSurface(sessionID)
	history = append(history, assistantMsg)

	turnTools := []string(nil)
	if st != nil {
		turnTools = st.turnTools
	}

	if completion != nil && len(completion.ToolCalls) > 0 && sess.IsWorkerChild() {
		calls := retainOfferedToolCalls(history, assistantMsg.ID, completion.ToolCalls, workerProseOfferedTools(sess))
		var batchTools []string
		history, batchTools, _, _, _, _, err = l.executeToolCallsInTurn(
			ctx, sess, sessionID, calls, in.ToolCtx, history, userPrompt, assistantMsg.ID, surfaceID, closeoutState,
		)
		if err != nil {
			return history, "", "", err
		}
		if st != nil {
			st.turnTools = append(st.turnTools, batchTools...)
		}
		assistantMsg.ToolCalls = answeredToolCalls(history, assistantMsg.ID, calls)
		history = patchAssistantToolCalls(history, assistantMsg.ID, assistantMsg.ToolCalls)
		if workerCompletionAccepted(history, assistantMsg.ID) {
			if err := l.closeCoordinatorDraftSlot(ctx, sessionID, st, assistantMsg.ID); err != nil {
				return history, "", "", err
			}
			return history, assistantMsg.ID, assistantMsg.Content, nil
		}
	} else if completion != nil && len(completion.ToolCalls) > 0 {
		// Coordinator closeout retains only settled receipts.
		assistantMsg.ToolCalls = answeredToolCalls(history, assistantMsg.ID, completion.ToolCalls)
		history = patchAssistantToolCalls(history, assistantMsg.ID, assistantMsg.ToolCalls)
		if l.Deps.UpdateMessage != nil {
			wire := assistantMsg
			wire.Content = closeoutWireContent(surfaceID)(assistantMsg.Content)
			_ = l.Deps.UpdateMessage(ctx, sessionID, assistantMsg.ID, wire)
		}
	}

	coercedContent, unread := l.maybeCoerceCloseoutContent(ctx, history, sessionID, surfaceID, assistantMsg.Content, assistantMsg.ID)
	assistantMsg.Content = coercedContent
	history, reject, blocked, err := l.tryRejectNoToolTurn(
		ctx, sess, history, userPrompt, coercedContent, surfaceID, turnTools, sessionID, assistantMsg.ID, st, false,
	)
	if err != nil {
		return history, "", "", err
	}
	if blocked {
		return l.emitEarlyCloseoutAfterFinishBlock(
			ctx, sess, sessionID, userPrompt, surfaceID, coercedContent, reject, st, history,
		)
	}

	if report, ok := guidance.ParseCoordinatorCompletionReport(assistantMsg.Content); ok && surface.SurfaceDeliversReport(surfaceID) {
		closeoutState := st
		if closeoutState == nil {
			closeoutState = &promptLoopTurnState{}
		}
		commitOut, cerr := l.handleAcceptedCloseoutReport(
			ctx, sess, sessionID, userPrompt, surfaceID, closeoutState, history, assistantMsg, guidance.CloseoutRead{Report: report, Unread: unread},
		)
		if cerr != nil {
			return history, "", "", cerr
		}
		history = commitOut.history
		if commitOut.endWithoutAssemble {
			return history, "", "", nil
		}
		if commitOut.committed {
			return history, commitOut.assistantMsg.ID, commitOut.assistantMsg.Content, nil
		}
		// Forced finish has no further iteration; assemble from the draft.
		return l.returnAssembledEarlyCloseout(
			ctx, sess, sessionID, userPrompt, surfaceID, commitOut.draftedContent, closeoutState.closeoutRetry.codes, closeoutState, history,
		)
	}

	committed, err := l.commitGuardedAssistantTurn(ctx, sess, sessionID, history, userPrompt, surfaceID, assistantMsg)
	if err != nil {
		return history, "", "", err
	}
	history[len(history)-1] = committed
	if err := l.closeCoordinatorDraftSlot(ctx, sessionID, st, committed.ID); err != nil {
		return history, "", "", err
	}
	return history, committed.ID, committed.Content, nil
}

func workerCompletionAccepted(history []api.Message, assistantMessageID string) bool {
	for _, msg := range history {
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		result := msg.ToolResult
		if strings.TrimSpace(result.AssistantMessageID) == strings.TrimSpace(assistantMessageID) &&
			result.Tool == workertools.CompleteLegTool && result.Outcome == api.ToolResultOutcomeCompleted {
			return true
		}
	}
	return false
}

func (l *PromptLoop) emitEarlyCloseoutAfterFinishBlock(
	ctx context.Context,
	sess *api.Session,
	sessionID, userPrompt, surfaceID, coercedContent string,
	reject *guidance.Refusal,
	st *promptLoopTurnState,
	history []api.Message,
) ([]api.Message, string, string, error) {
	closeoutState := st
	if closeoutState == nil {
		closeoutState = &promptLoopTurnState{}
	}
	drafted := strings.TrimSpace(coercedContent)
	forcedBy := closeoutState.closeoutRetry.codes
	if code := reject.Code(); code != "" {
		forcedBy = append(append([]string(nil), forcedBy...), code)
	}
	return l.returnAssembledEarlyCloseout(
		ctx, sess, sessionID, userPrompt, surfaceID, drafted, forcedBy, closeoutState, history,
	)
}

func (l *PromptLoop) returnAssembledEarlyCloseout(
	ctx context.Context,
	sess *api.Session,
	sessionID, userPrompt, surfaceID, draftedContent string,
	forcedBy []string,
	st *promptLoopTurnState,
	history []api.Message,
) ([]api.Message, string, string, error) {
	emitOut, err := l.emitAssembledCloseout(
		ctx, sess, sessionID, userPrompt, surfaceID, draftedContent, forcedBy, st, history,
	)
	if err != nil {
		return history, "", "", err
	}
	if emitOut.committed {
		return emitOut.history, emitOut.assistantMsg.ID, emitOut.assistantMsg.Content, nil
	}
	if st != nil {
		st.turnEndedGuidanceReject = true
		if err := l.closeCoordinatorDraftSlot(ctx, sessionID, st, st.draftSlotID); err != nil {
			return emitOut.history, "", "", err
		}
	}
	return emitOut.history, "", "", nil
}
