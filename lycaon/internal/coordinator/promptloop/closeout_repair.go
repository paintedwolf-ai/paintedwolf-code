package promptloop

import (
	"github.com/lycaon/lycaon/internal/toolreach"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/pkg/api"
)

// closeoutRepairNeeded binds repair to the exact rejected assistant occurrence.
type closeoutRepairNeeded struct {
	refusal   *guidance.Refusal
	messageID string
}

func (e *closeoutRepairNeeded) Error() string { return e.refusal.Error() }
func (e *closeoutRepairNeeded) Unwrap() error { return e.refusal }

// IsCloseoutBlocked reports whether the prompt loop settled as closeout_blocked.
func (r *PromptRunResult) IsCloseoutBlocked() bool {
	return r != nil && r.Outcome == promptresult.OutcomeCloseoutBlocked
}

func (l *PromptLoop) prepareCloseoutRepair(ctx context.Context, setup *promptLoopSetup, err error) (bool, error) {
	var repair *closeoutRepairNeeded
	if !errors.As(err, &repair) || setup.sess.IsWorkerChild() {
		return false, nil
	}
	st := setup.state
	history := make([]api.Message, 0, len(st.history))
	for _, message := range st.history {
		if message.ID != repair.messageID {
			history = append(history, message)
		}
	}
	var appendErr error
	st.history, appendErr = l.Nudges.appendHostNudge(ctx, setup.sessionID, history, HostNudge{
		Content:  repair.refusal.Body,
		Feedback: &api.ToolFeedback{Code: repair.refusal.Code(), Details: refusalDetails(repair.refusal)},
	}, api.MessageKindCoordinatorGuidance, st)
	if appendErr != nil {
		return false, appendErr
	}
	// One acting round followed by a bounded closeout. Run applies this once;
	// normal spend, permission, runnable-state and completion guards still apply.
	setup.completed += st.turnsRanThisRun
	setup.maxIter = setup.completed + 2
	st.progress().SetMaxToolLoops(setup.maxIter)
	st.proseFinish = false
	st.proseFinishDelivered = false
	st.lastAssistantID = ""
	st.lastAssistantContent = ""
	return true, nil
}

// settleCloseoutBlocked converts a stopped gated closeout into a typed
// closeout_blocked turn outcome with a host-written notice on the transcript
// and workflow wire while keeping the workflow resumable at its current phase.
func (l *turnCloseout) settleCloseoutBlocked(
	ctx context.Context,
	_ PromptRunInput,
	setup *promptLoopSetup,
	err error,
) (*PromptRunResult, error) {
	if !errors.Is(err, guidance.ErrCloseoutStopped) || setup == nil || setup.state == nil || (setup.sess != nil && setup.sess.IsWorkerChild()) {
		return nil, err
	}
	refusal, ok := guidance.RefusalFromError(err)
	if !ok || refusal == nil || strings.TrimSpace(refusal.Code()) == "" {
		return nil, err
	}
	st := setup.state
	if l.Nudges != nil && strings.TrimSpace(refusal.Body) != "" && l.Projection != nil && l.Projection.Deps.AppendMessages != nil {
		if updated, nudgeErr := l.Nudges.appendHostNudge(ctx, setup.sessionID, st.history, HostNudge{
			Content:  refusal.Body,
			Feedback: &api.ToolFeedback{Code: refusal.Code(), Details: refusalDetails(refusal)},
		}, api.MessageKindCoordinatorGuidance, st); nudgeErr == nil {
			st.history = updated
		}
		_ = l.Nudges.closeCoordinatorDraftSlot(ctx, setup.sessionID, st, st.draftSlotID)
	}
	blocked := buildCloseoutBlocked(refusal, st.coordinatorFrame)
	noticeMsg := api.Message{
		ID:            uuid.NewString(),
		Role:          api.MessageRoleSystem,
		Origin:        api.MessageOriginHost,
		Authority:     api.ContentAuthoritySystem,
		TrustTier:     api.ContentTrustTierTrusted,
		Kind:          api.MessageKindHostNudge,
		HostSignalID:  blocked.Code,
		WorkflowRunID: strings.TrimSpace(st.coordinatorFrame.RunContext.RunID),
		Visibility:    api.MessageVisibilityTranscript,
		Content:       blocked.Notice,
		CreatedAt:     time.Now().UTC(),
	}
	if l.Projection != nil && l.Projection.Deps.AppendMessages != nil {
		if appendErr := l.Projection.Deps.AppendMessages(ctx, setup.sessionID, noticeMsg); appendErr != nil {
			return nil, appendErr
		}
	}
	st.history = append(st.history, noticeMsg)
	if l.Projection != nil && l.Projection.Deps.Events != nil {
		projectID := ""
		if setup.sess != nil {
			projectID = setup.sess.ProjectID
		}
		l.Projection.Deps.Events.PublishGatePending(ctx, projectID, setup.sessionID, api.WorkflowEvent{
			Event:             api.WorkflowEventKindGatePending,
			WorkflowID:        strings.TrimSpace(st.coordinatorFrame.RunContext.WorkflowID),
			WorkflowRunID:     strings.TrimSpace(st.coordinatorFrame.RunContext.RunID),
			Phase:             blocked.Phase,
			Status:            string(promptresult.OutcomeCloseoutBlocked),
			BranchInstruction: blocked.Notice,
		})
	}
	return &PromptRunResult{
		LastAssistantID:      noticeMsg.ID,
		LastOutputID:         st.output.id,
		LastAssistantContent: blocked.Notice,
		TurnTools:            st.turnTools,
		Reach:                toolreach.Freeze(st.offeredToolNames, st.turnToolPlan),
		TasksDispatchedCount: st.tasksDispatchedCount,
		Outcome:              promptresult.OutcomeCloseoutBlocked,
		CloseoutBlocked:      blocked,
	}, nil
}

func refusalDetails(refusal *guidance.Refusal) map[string]any {
	return refusal.Facts.FeedbackFor(refusal.Code()).Details
}

func buildCloseoutBlocked(refusal *guidance.Refusal, frame inject.CoordinatorTurnFrame) *promptresult.CloseoutBlocked {
	code := strings.TrimSpace(refusal.Code())
	details := refusalDetails(refusal)
	phase, _ := details["phase"].(string)
	if phase = strings.TrimSpace(phase); phase == "" {
		phase = strings.TrimSpace(frame.RunContext.CurrentPhase)
	}
	rawLeaves, _ := details["closeout_gate_open_leaves"].(string)
	openLeaves := parseCloseoutGateLeaves(rawLeaves)
	if len(openLeaves) == 0 {
		openLeaves = parseCloseoutGateLeaves(strings.Join(frame.RunContext.FailedLeaves, ","))
	}
	return &promptresult.CloseoutBlocked{
		Code:           code,
		Phase:          phase,
		OpenGateLeaves: openLeaves,
		Notice:         formatCloseoutBlockedNotice(code, phase, openLeaves),
	}
}

func parseCloseoutGateLeaves(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if leaf := strings.TrimSpace(p); leaf != "" {
			out = append(out, leaf)
		}
	}
	return out
}

func formatCloseoutBlockedNotice(code, phase string, openLeaves []string) string {
	leaves := strings.Join(openLeaves, ", ")
	if phase != "" && leaves != "" {
		return fmt.Sprintf("Workflow phase %s is waiting on open gate(s): %s (%s). Send a message or resolve the open gate to continue.", phase, leaves, code)
	}
	return fmt.Sprintf("Workflow closeout is waiting on open requirements (%s). Send a message or resolve the open requirement to continue.", code)
}
