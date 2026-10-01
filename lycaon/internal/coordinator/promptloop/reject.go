package promptloop

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// maybeCoerceCloseoutContent stores a report surface's draft as its envelope
// and returns the members of it the report did not take.
func (l *PromptLoop) maybeCoerceCloseoutContent(
	ctx context.Context,
	history []api.Message,
	sessionID, surfaceID, content, assistantMessageID string,
) (string, []jsonshape.Issue) {
	pinned := l.pinnedCloseoutSynthesis(ctx, sessionID)
	prepared, read, ok := guard.PrepareCoordinatorCloseoutContent(surfaceID, content, pinned)
	if !ok {
		return content, nil
	}
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	for i := range history {
		if history[i].ID == assistantMessageID {
			history[i].Content = prepared
			break
		}
	}
	return prepared, read.Unread
}

func (l *PromptLoop) tryRejectNoToolTurn(
	ctx context.Context,
	sess *api.Session,
	history []api.Message,
	userPrompt string,
	lastAssistantContent string,
	surfaceID string,
	turnTools []string,
	sessionID string,
	assistantMessageID string,
	st *promptLoopTurnState,
	invokeAllowed bool,
) (hist []api.Message, reject *guidance.Refusal, blocked bool, err error) {
	if l.Deps.BeforeFinishNoToolTurn == nil {
		return history, nil, false, nil
	}
	workersIdle := true
	if l.Deps.ImplementSessionState != nil {
		state := l.Deps.ImplementSessionState(ctx, sess)
		workersIdle = state.WorkersInFlight == 0
	}
	reject, block := l.Deps.BeforeFinishNoToolTurn(ctx, sess, history, userPrompt, lastAssistantContent, surfaceID, workersIdle, turnTools, invokeAllowed)
	if !block {
		return history, nil, false, nil
	}
	draftSlotID := ""
	if st != nil && st.usesCoordinatorDraftSlot(assistantMessageID) {
		draftSlotID = st.draftSlotID
	}
	history, err = l.rejectBlockedAssistantTurn(ctx, sessionID, history, assistantMessageID, draftSlotID, reject, st)
	if err != nil {
		return nil, reject, false, err
	}
	return history, reject, true, nil
}

// rejectBlockedAssistantTurn removes rejected assistant prose from the transcript
// and appends one guidance nudge for the model to read and retry.
func (l *PromptLoop) rejectBlockedAssistantTurn(
	ctx context.Context,
	sessionID string,
	history []api.Message,
	assistantMessageID string,
	draftSlotID string,
	reject *guidance.Refusal,
	st *promptLoopTurnState,
) ([]api.Message, error) {
	history, err := l.retractRejectedAssistantTurn(ctx, sessionID, history, assistantMessageID, draftSlotID, reject)
	if err != nil {
		return nil, err
	}
	return l.appendHostNudge(ctx, sessionID, history, HostNudge{
		Content:  reject.Body,
		Feedback: &api.ToolFeedback{Code: reject.Code(), Details: reject.Facts.FeedbackFor(reject.Code()).Details},
	}, "", st)
}

func (l *PromptLoop) retractRejectedAssistantTurn(
	ctx context.Context,
	sessionID string,
	history []api.Message,
	assistantMessageID string,
	draftSlotID string,
	reject *guidance.Refusal,
) ([]api.Message, error) {
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	if assistantMessageID == "" {
		return history, nil
	}
	rejectedBody := ""
	for i := range history {
		if history[i].ID == assistantMessageID {
			rejectedBody = history[i].Content
			break
		}
	}
	// Save the rejected body before retracting its transcript row.
	if l.Deps.AppendDraftVersion == nil {
		return nil, fmt.Errorf("append draft version not configured")
	}
	if _, err := l.Deps.AppendDraftVersion(ctx, sessionID, assistantMessageID, rejectedBody, reject.Code()); err != nil {
		return nil, err
	}
	if draftSlotID != "" && assistantMessageID == draftSlotID {
		if l.Deps.UpdateMessage != nil {
			// Retain the rejected draft until the retry supplies replacement content.
			reset := newProvisionalAssistantMessage(api.Message{
				ID:          draftSlotID,
				Content:     rejectedBody,
				DraftStatus: api.DraftStatusLive,
			})
			if err := l.stampDraftVersionCount(ctx, sessionID, &reset); err != nil {
				return nil, err
			}
			if err := l.Deps.UpdateMessage(ctx, sessionID, draftSlotID, reset); err != nil {
				return nil, err
			}
		}
	} else {
		if l.Deps.UpdateMessage == nil {
			return nil, fmt.Errorf("update message not configured")
		}
		patch := newProvisionalAssistantMessage(api.Message{
			ID:          assistantMessageID,
			Content:     rejectedBody,
			DraftStatus: api.DraftStatusRejected,
		})
		if err := l.Deps.UpdateMessage(ctx, sessionID, assistantMessageID, patch); err != nil {
			return nil, err
		}
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].ID != assistantMessageID {
			continue
		}
		return append(append([]api.Message(nil), history[:i]...), history[i+1:]...), nil
	}
	return history, nil
}

// Intrinsic refusals retain occurrence identity and the offered recovery surface.
func (l *PromptLoop) rejectToolOccurrence(ctx context.Context, sess *api.Session, tc api.ToolCall, toolCtx tools.ToolContext, code string, data map[string]any) *guidance.Refusal {
	ctx = tools.WithRecoveryTools(ctx, toolCtx.TurnOfferedToolNames)
	ctx = curationctx.WithSession(ctx, curationctx.Session{SessionID: sess.ID, ProjectID: sess.ProjectID, OwnerPersonID: sess.OwnerPersonID, Posture: string(sess.Posture), Agent: toolCtx.Agent})
	data = maps.Clone(data)
	if data == nil {
		data = map[string]any{}
	}
	data["tool"], data["profile"], data["turn_surface"] = tc.Name, toolCtx.Agent, toolCtx.TurnSurfaceID
	if code == "TOOL_NOT_OFFERED" {
		loadable := toolCtx.TurnToolPlan.Deferred(tc.Name) && tools.ToolOffered(ctx, "request_tools")
		data["tool_loadable"] = loadable
		if loadable {
			data["replacement_calls"] = []tools.ReplacementCall{{Tool: "request_tools", Args: map[string]any{"need": tc.Name}}}
		}
	}
	tr := &tools.ToolReject{Code: code, FailureClass: api.FailureClassPolicyRejection, Data: data}
	if code == tools.ToolOwnerFailedCode {
		tr.FailureClass = api.FailureClassOwnerError
		tr.Data["reason"] = "tool definition unavailable"
	}
	err := l.Deps.BlockPlane.RejectObservation(ctx, tc.Name, toolCtx.Agent, tc.Args, tr)
	if err == nil {
		err = tools.RenderReject(tr, l.Deps.RejectFmt)
	}
	if refusal, ok := guidance.RefusalFromError(err); ok {
		return refusal
	}
	return l.toolReject(code, tr.Data).WithCause(tr)
}

// toolReject renders a refusal with a structured code.
func (l *PromptLoop) toolReject(code string, data map[string]any) *guidance.Refusal {
	if l.Deps.RejectFmt != nil {
		if formatted, err := l.Deps.RejectFmt.Format(code, data); err == nil && strings.TrimSpace(formatted) != "" {
			return guidance.NewRefusal(code, formatted).WithDetails(data, nil)
		}
	}
	return guidance.NewRefusal(code, fmt.Sprintf("%s %s\n%s %s", hostmarker.Rejected, code, hostmarker.CodeLine, code)).WithDetails(data, nil)
}

// toolRejectMessage builds a rejected tool row. Outcome is rejected by construction.
func (l *PromptLoop) toolRejectMessage(toolName, toolCallID, assistantMessageID string, toolArgs map[string]any, reject *guidance.Refusal) api.Message {
	facts := guidance.ToolResultFacts{}
	content := ""
	if reject != nil {
		facts = reject.Facts
		content = reject.Body
	}
	return l.toolResultMessage(toolName, toolCallID, assistantMessageID, toolArgs, content,
		facts.WithOutcome(api.ToolResultOutcomeRejected))
}

// toolResultMessage builds a host-authored tool row with its stated outcome.
func (l *PromptLoop) toolResultMessage(toolName, toolCallID, assistantMessageID string, toolArgs map[string]any, content string, facts guidance.ToolResultFacts) api.Message {
	toolResult := guidance.ComposeToolResult(content, facts, l.Deps.HintConfig)
	if toolResult == nil {
		toolResult = &api.ToolResult{Content: content, Outcome: facts.Resolution()}
	}
	toolResult.Tool = toolName
	toolResult.ToolCallID = strings.TrimSpace(toolCallID)
	toolResult.AssistantMessageID = strings.TrimSpace(assistantMessageID)
	toolResult.ToolArgs = toolArgs
	return api.Message{
		ID:         uuid.NewString(),
		Role:       api.MessageRoleTool,
		Content:    content,
		ToolResult: toolResult,
		CreatedAt:  time.Now().UTC(),
	}
}

func (l *PromptLoop) appendInFlightWorkerRosterNote(
	ctx context.Context,
	sessionID string,
	sess *api.Session,
	history []api.Message,
	taskEnqueuedThisTurn int,
	lastTaskMessageID string,
) error {
	if taskEnqueuedThisTurn < 2 || lastTaskMessageID == "" || l.Deps.InFlightWorkerRosterNote == nil {
		return nil
	}
	roster := strings.TrimSpace(l.Deps.InFlightWorkerRosterNote(ctx, sess))
	if roster == "" {
		return nil
	}
	for i := range history {
		if history[i].ID != lastTaskMessageID {
			continue
		}
		history[i].Content += roster
		if history[i].ToolResult != nil {
			result := *history[i].ToolResult
			result.Content += roster
			facts := guidance.ToolResultFacts{Codes: result.Codes, Feedback: result.Feedback}.
				WithCode("BANNER_WORKER_INFLIGHT_ROSTER")
			result.Codes, result.Feedback = facts.Codes, facts.Feedback
			history[i].ToolResult = &result
		}
		if l.Deps.UpdateMessage != nil {
			return l.Deps.UpdateMessage(ctx, sessionID, lastTaskMessageID, history[i])
		}
		break
	}
	return nil
}

func isTaskToolName(tool string) bool {
	return strings.TrimSpace(strings.ToLower(tool)) == "task"
}

// taskSpawnCommitted is a successful task() enqueue.
func taskSpawnCommitted(toolName string, result *api.ToolResult, succeeded bool) bool {
	if !succeeded || !isTaskToolName(toolName) {
		return false
	}
	return result != nil && result.Outcome == api.ToolResultOutcomeCompleted && result.Dispatch != nil && strings.TrimSpace(result.Dispatch.WorkerID) != ""
}

func (l *PromptLoop) overlayIntegrateRejectEndsToolLoop(
	ctx context.Context,
	sess *api.Session,
	sessionID string,
	rejectCode string,
) bool {
	if l.Deps.ImplementSessionState == nil {
		return false
	}
	state := l.Deps.ImplementSessionState(ctx, sess)
	if !guard.OverlayIntegrateRejectEndsToolLoop(rejectCode, state) {
		return false
	}
	if l.Deps.ReconcileCoordinatorBatch != nil {
		l.Deps.ReconcileCoordinatorBatch(ctx, sessionID)
	}
	return true
}
