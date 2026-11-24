package promptloop

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/completioncue"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const planCompletionCueMessageID = "host-plan-completion-cue"

func currentUserIntentID(history []api.Message) string {
	intent, ok := api.LastUserIntentMessage(history)
	if !ok {
		return ""
	}
	return strings.TrimSpace(intent.ID)
}

func canActAndUpdateProgress(callable []tools.ToolMeta) bool {
	hasUpdateProgress := false
	hasActionTool := false
	for _, meta := range callable {
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			continue
		}
		if name == "update_progress" {
			hasUpdateProgress = true
		} else {
			hasActionTool = true
		}
	}
	return hasUpdateProgress && hasActionTool
}

func (l *modelTurn) appendCompletionCue(
	ctx context.Context,
	sess *api.Session,
	sessionID, profileID string,
	history, messages []api.Message,
	callable []tools.ToolMeta,
	toolUse modelcall.ToolUseMode,
	st *promptLoopTurnState,
) []api.Message {
	if l == nil || l.Context == nil || l.Context.Deps.CompletionCues == nil {
		return messages
	}
	if sess == nil || sess.IsWorkerChild() || !guard.IsCoordinatorProfile(profileID) {
		return messages
	}
	intentID := currentUserIntentID(history)
	var binding completioncue.DecisionBinding
	if st != nil && st.decisionCue != nil && st.decisionCue.IntentID == intentID {
		binding = *st.decisionCue
	} else {
		ordinary := (st == nil || !st.proseFinish) &&
			toolUse == modelcall.ToolUseAllowed &&
			canActAndUpdateProgress(callable)
		workflowRunID := ""
		if st != nil {
			workflowRunID = strings.TrimSpace(st.coordinatorFrame.RunContext.RunID)
		}
		var pendingCount func(context.Context) (int, bool)
		if l.Context.Deps.PendingProgressCount != nil {
			pendingCount = func(ctx context.Context) (int, bool) {
				return l.Context.Deps.PendingProgressCount(ctx, sess)
			}
		}
		var confirmCheck func(context.Context, completioncue.CheckFact) bool
		if l.Context.Deps.ConfirmCheckCurrent != nil {
			confirmCheck = func(ctx context.Context, check completioncue.CheckFact) bool {
				return l.Context.Deps.ConfirmCheckCurrent(ctx, sess, check)
			}
		}
		binding = l.Context.Deps.CompletionCues.SelectForDecision(ctx, completioncue.SelectInput{
			SessionID:           sessionID,
			WorkflowRunID:       workflowRunID,
			IntentID:            intentID,
			OrdinaryDecision:    ordinary,
			PendingCount:        pendingCount,
			ConfirmCheckCurrent: confirmCheck,
		})
		if st != nil {
			copied := binding
			st.decisionCue = &copied
		}
	}
	if binding.Code == "" {
		return messages
	}
	content := l.Closeout.Deps.HintConfig.ResolveHintMessage(binding.Code)
	if content == "" {
		return messages
	}
	return append(messages, api.Message{
		ID:           planCompletionCueMessageID,
		Role:         api.MessageRoleSystem,
		Content:      content,
		Origin:       api.MessageOriginHost,
		Authority:    api.ContentAuthoritySystem,
		TrustTier:    api.ContentTrustTierTrusted,
		HostSignalID: binding.Code,
	})
}

func (l *modelTurn) commitCompletionCue(ctx context.Context, st *promptLoopTurnState) error {
	if l == nil || l.Context == nil || l.Context.Deps.CompletionCues == nil || st == nil || st.decisionCue == nil {
		return nil
	}
	binding := *st.decisionCue
	st.decisionCue = nil
	return l.Context.Deps.CompletionCues.CommitDecision(ctx, binding)
}

func (l *PromptLoop) observeBatchCompletionCue(
	sess *api.Session,
	sessionID string,
	st *promptLoopTurnState,
	history []api.Message,
	batchResult toolBatchOutcome,
) {
	if l == nil || l.Context == nil || l.Context.Deps.CompletionCues == nil {
		return
	}
	if sess == nil || sess.IsWorkerChild() {
		return
	}
	intentID := currentUserIntentID(history)
	workflowRunID := ""
	if st != nil {
		workflowRunID = strings.TrimSpace(st.coordinatorFrame.RunContext.RunID)
	}
	if batchResult.planItemCompleted {
		l.Context.Deps.CompletionCues.RecordPlanItemCompleted(sessionID, workflowRunID, intentID)
	}
	if batchResult.passingCheck != nil {
		l.Context.Deps.CompletionCues.RecordPassingCheck(*batchResult.passingCheck)
	}
}

func checkFactFromRun(sess *api.Session, sessionID, toolName string, run toolInvocation) *completioncue.CheckFact {
	if sess == nil || sess.IsWorkerChild() || !run.succeeded() || run.captures.sourceRun == nil {
		return nil
	}
	if toolName != "verify" && toolName != "command" {
		return nil
	}
	sr := run.captures.sourceRun
	if !sr.IsCheck || strings.TrimSpace(sr.CheckID) == "" || sr.Verdict != api.SourceVerdictPassed {
		return nil
	}
	if strings.TrimSpace(sr.UserIntentID) == "" {
		return nil
	}
	return &completioncue.CheckFact{
		CheckID:          strings.TrimSpace(sr.CheckID),
		SessionID:        strings.TrimSpace(sessionID),
		WorkflowRunID:    strings.TrimSpace(sr.WorkflowRunID),
		IntentID:         strings.TrimSpace(sr.UserIntentID),
		SourceRevision:   sr.SourceRevision,
		SourceRootDigest: sr.SourceRootDigest,
	}
}
