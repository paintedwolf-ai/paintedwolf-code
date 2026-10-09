package promptloop

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolcommand"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/spawn"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/pkg/api"
)

// turnCloseout ends a turn: closeout nudges, early closeouts, and the grounded closeout report.

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
	if toolcommand.IsNativeCommandRedirectCode(code) {
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

// TurnCloseoutCause is what ended the turn early; a blocked loop also names
// the call the fuse tripped on.
type TurnCloseoutCause struct {
	Reason       TurnCloseoutReason
	CancelReason string
	BlockedTool  string
	BlockedCode  string
	BlockedCount int
}

// Text states the cause in the clause the closeout nudge opens with.
func (c TurnCloseoutCause) Text() string {
	if c.Reason != TurnCloseoutBlockedLoop || c.BlockedCount <= 0 {
		return TurnCloseoutReasonText(c.Reason)
	}
	subject := "the reply"
	if c.BlockedTool != "" {
		subject = "`" + c.BlockedTool + "`"
	}
	text := fmt.Sprintf("%s was refused %d times in a row", subject, c.BlockedCount)
	if c.BlockedCode != "" {
		text += " (" + c.BlockedCode + ")"
	}
	return text
}

// HostNudge carries rendered guidance and its anchor ID.
type HostNudge struct {
	Content  string
	SignalID string
	Feedback *api.ToolFeedback
}

func (n HostNudge) Empty() bool { return strings.TrimSpace(n.Content) == "" }

// TurnCloseoutNudge renders the internal user nudge for a prose-only finish turn.
type TurnCloseoutNudge func(ctx context.Context, sess *api.Session, profileID string, cause TurnCloseoutCause) HostNudge

// IterationRunwayNudge renders the one-shot heads-up when few tool iterations remain.
type IterationRunwayNudge func(ctx context.Context, sess *api.Session, profileID string, remaining int) HostNudge

// IterationRunwayThreshold reserves final iterations for handoff on longer
// coordinator turns; a worker's runway follows spawn.WorkerRunway.
const IterationRunwayThreshold = 10

func (l *turnCloseout) maybeTurnCloseout(
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
	return l.appendTurnCloseoutNudge(ctx, sess, sessionID, profileID, history, TurnCloseoutCause{Reason: TurnCloseoutIterationCap}, st)
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

func (l *turnCloseout) maybeIterationRunwayNudge(
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
	return l.Nudges.appendHostNudge(ctx, sessionID, history, nudge, "", st)
}

// applyLandingAdvisories appends closeout and runway guidance in order.
func (l *turnCloseout) applyLandingAdvisories(
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

	st.history, err = l.Nudges.maybeWorkerBudgetAnswerNudge(ctx, sess, sessionID, st.history, step, maxIter, st)
	if err != nil {
		return closeoutNudgeSent, err
	}
	st.history, err = l.maybeIterationRunwayNudge(ctx, sess, sessionID, profileID, st.history, step, maxIter, st)
	if err != nil {
		return closeoutNudgeSent, err
	}
	st.history, err = l.Nudges.maybeSurveyStreakNudge(ctx, sess, sessionID, st.history, st)
	if err != nil {
		return closeoutNudgeSent, err
	}
	st.history, err = l.Nudges.maybeSpendRunwayNudge(ctx, sess, sessionID, st.history, spendRunway, st)
	if err != nil {
		return closeoutNudgeSent, err
	}
	st.history, err = l.Nudges.maybeSpendSoftStopNudge(ctx, sess, sessionID, st.history, spendWindDown, st)
	return closeoutNudgeSent, err
}

func (l *turnCloseout) appendTurnCloseoutNudge(
	ctx context.Context,
	sess *api.Session,
	sessionID, profileID string,
	history []api.Message,
	cause TurnCloseoutCause,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	nudge := l.turnCloseoutNudge(ctx, sess, profileID, cause)
	if nudge.Empty() {
		return history, nil
	}
	if historyHasInternalNudge(history, nudge.Content) {
		return history, nil
	}
	kind := api.MessageKind("")
	if cause.Reason == TurnCloseoutIterationCap {
		kind = api.MessageKindIterationCapCloseout
	}
	return l.Nudges.appendHostNudge(ctx, sessionID, history, nudge, kind, st)
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

func (l *turnCloseout) turnCloseoutNudge(
	ctx context.Context,
	sess *api.Session,
	profileID string,
	cause TurnCloseoutCause,
) HostNudge {
	if l == nil || l.Deps.TurnCloseoutNudge == nil {
		return HostNudge{}
	}
	nudge := l.Deps.TurnCloseoutNudge(ctx, sess, profileID, cause)
	nudge.Content = strings.TrimSpace(nudge.Content)
	return nudge
}

func (l *turnCloseout) runEarlyTurnCloseout(
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
	cause := st.closeoutCause(reason, cancelReason)
	var err error
	if !skipNudge {
		history, err = l.appendTurnCloseoutNudge(ctx, sess, sessionID, profileID, history, cause, st)
		if err != nil {
			return history, "", "", err
		}
	}
	closeoutState := st
	if closeoutState == nil {
		closeoutState = &promptLoopTurnState{}
	}
	closeoutState.proseFinish = true
	closeoutState.closeoutCauseText = cause.Text()
	iterIndex := closeoutState.turnsRanThisRun
	assistantMsg, completion, _, err := l.Model.runAssistantStreamTurn(ctx, sessionID, sess, history, closeoutState, profileID, userPrompt, iterIndex, maxIter, in.HostTurn)
	if err != nil {
		return history, "", "", err
	}
	if st != nil {
		st.proseFinishDelivered = true
	}
	surfaceID := l.Context.promptTurnSurface(sessionID)
	history = append(history, assistantMsg)

	turnTools := []string(nil)
	if st != nil {
		turnTools = st.turnTools
	}

	if completion != nil && len(completion.ToolCalls) > 0 && sess.IsWorkerChild() {
		calls := retainOfferedToolCalls(history, assistantMsg.ID, completion.ToolCalls, workerProseOfferedTools(sess))
		var batchTools []string
		history, batchTools, _, _, _, _, err = l.Batch.executeToolCallsInTurn(
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
			if err := l.Nudges.closeCoordinatorDraftSlot(ctx, sessionID, st, assistantMsg.ID); err != nil {
				return history, "", "", err
			}
			return history, assistantMsg.ID, assistantMsg.Content, nil
		}
	} else if completion != nil && len(completion.ToolCalls) > 0 {
		// Coordinator closeout retains only settled receipts.
		assistantMsg.ToolCalls = answeredToolCalls(history, assistantMsg.ID, completion.ToolCalls)
		history = patchAssistantToolCalls(history, assistantMsg.ID, assistantMsg.ToolCalls)
		if l.Projection.Deps.UpdateMessage != nil {
			wire := assistantMsg
			wire.Content = closeoutWireContent(surfaceID)(assistantMsg.Content)
			_ = l.Projection.Deps.UpdateMessage(ctx, sessionID, assistantMsg.ID, wire)
		}
	}

	coercedContent, unread := l.Tools.maybeCoerceCloseoutContent(ctx, history, sessionID, surfaceID, assistantMsg.Content, assistantMsg.ID)
	assistantMsg.Content = coercedContent
	history, reject, blocked, err := l.Tools.tryRejectNoToolTurn(
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

	committed, err := l.Projection.commitGuardedAssistantTurn(ctx, sess, sessionID, history, userPrompt, surfaceID, assistantMsg)
	if err != nil {
		return history, "", "", err
	}
	history[len(history)-1] = committed
	if err := l.Nudges.closeCoordinatorDraftSlot(ctx, sessionID, st, committed.ID); err != nil {
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

func (l *turnCloseout) emitEarlyCloseoutAfterFinishBlock(
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

func (l *turnCloseout) returnAssembledEarlyCloseout(
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
		if err := l.Nudges.closeCoordinatorDraftSlot(ctx, sessionID, st, st.draftSlotID); err != nil {
			return emitOut.history, "", "", err
		}
	}
	return emitOut.history, "", "", nil
}

// turnNudges keeps the host's in-turn guidance: nudges, spend and budget runways, and repeat-call fuses.

// appendUserNudge defers persistence during an open draft slot.
func (l *turnNudges) appendUserNudge(
	ctx context.Context,
	sessionID string,
	history []api.Message,
	content string,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	return l.appendHostNudge(ctx, sessionID, history, HostNudge{Content: content}, "", st)
}

func newHostNudge(nudge HostNudge, kind api.MessageKind) api.Message {
	signalID := strings.TrimSpace(nudge.SignalID)
	if nudge.Feedback != nil {
		signalID = feedbackSignalID(*nudge.Feedback)
	}
	return api.Message{
		ID:        uuid.NewString(),
		Role:      api.MessageRoleUser,
		Content:   nudge.Content,
		Origin:    api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		Kind:         kind,
		HostSignalID: signalID,
		Visibility:   api.MessageVisibilityInternal,
		CreatedAt:    time.Now().UTC(),
	}
}

func (l *turnNudges) appendHostNudge(
	ctx context.Context,
	sessionID string,
	history []api.Message,
	hostNudge HostNudge,
	kind api.MessageKind,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	hostNudge.Content = strings.TrimSpace(hostNudge.Content)
	if hostNudge.Content == "" {
		return history, nil
	}
	if i := lastHostNudgeWithSignal(history, hostNudge); i >= 0 {
		history[i].Content = hostNudge.Content
		history[i].CreatedAt = time.Now().UTC()
		if replaceDeferredUserNudge(st, history[i]) {
			return history, nil
		}
		if l.Projection.Deps.UpdateMessage != nil {
			if err := l.Projection.Deps.UpdateMessage(ctx, sessionID, history[i].ID, history[i]); err != nil {
				return nil, err
			}
		}
		return history, nil
	}
	nudge := newHostNudge(hostNudge, kind)
	history = append(history, nudge)
	if deferUserNudgeStore(st) {
		st.deferredUserNudges = append(st.deferredUserNudges, nudge)
		return history, nil
	}
	if l.Projection.Deps.AppendMessages == nil {
		return nil, fmt.Errorf("append messages not configured")
	}
	if err := l.Projection.Deps.AppendMessages(ctx, sessionID, nudge); err != nil {
		return nil, err
	}
	return history, nil
}

func lastHostNudgeWithSignal(history []api.Message, nudge HostNudge) int {
	signalID := strings.TrimSpace(nudge.SignalID)
	if nudge.Feedback != nil {
		signalID = feedbackSignalID(*nudge.Feedback)
	}
	if signalID == "" {
		return -1
	}
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Origin != api.MessageOriginHost {
			continue
		}
		if strings.TrimSpace(msg.HostSignalID) == signalID {
			return i
		}
	}
	return -1
}

func feedbackSignalID(feedback api.ToolFeedback) string {
	code := strings.TrimSpace(feedback.Code)
	if code == "" {
		return ""
	}
	if feedback.Subject == nil {
		return "guidance:" + code
	}
	kind := strings.TrimSpace(feedback.Subject.Kind)
	id := strings.TrimSpace(feedback.Subject.ID)
	if kind == "" || id == "" {
		return "guidance:" + code
	}
	return "guidance:" + code + ":" + kind + ":" + id
}

func replaceDeferredUserNudge(st *promptLoopTurnState, msg api.Message) bool {
	if st == nil {
		return false
	}
	for i := range st.deferredUserNudges {
		if st.deferredUserNudges[i].ID == msg.ID {
			st.deferredUserNudges[i] = msg
			return true
		}
	}
	return false
}

func deferUserNudgeStore(st *promptLoopTurnState) bool {
	return st != nil && st.draftSlotID != "" && st.draftSlotAppended
}

func (l *turnNudges) flushDeferredUserNudges(ctx context.Context, sessionID string, st *promptLoopTurnState) error {
	if l == nil || st == nil || len(st.deferredUserNudges) == 0 {
		return nil
	}
	if l.Projection.Deps.AppendMessages == nil {
		return fmt.Errorf("append messages not configured")
	}
	pending := make([]api.Message, 0, len(st.deferredUserNudges))
	for _, nudge := range st.deferredUserNudges {
		nudge.Content = strings.TrimSpace(nudge.Content)
		if nudge.Content == "" {
			continue
		}
		pending = append(pending, nudge)
	}
	if len(pending) == 0 {
		st.deferredUserNudges = nil
		return nil
	}
	if err := l.Projection.Deps.AppendMessages(ctx, sessionID, pending...); err != nil {
		return err
	}
	st.deferredUserNudges = nil
	return nil
}

func (l *turnNudges) closeCoordinatorDraftSlot(ctx context.Context, sessionID string, st *promptLoopTurnState, messageID string) error {
	if st != nil {
		st.closeCoordinatorDraftSlot(messageID)
	}
	return l.flushDeferredUserNudges(ctx, sessionID, st)
}
