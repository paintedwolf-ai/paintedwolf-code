package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

// closeoutRetryState is the current prompt's projection of retained closeout
// recovery. Citation and report-document refusals keep separate attempt counts:
// they spend different budgets.
type closeoutRetryState struct {
	attempt         int
	documentAttempt int
	prevKey         string
	codes           []string
}

type closeoutCommitOutcome struct {
	// Terminal exits retain the complete report, including its citation fields.
	draftedContent string
	committed      bool
	retry          bool
	exhausted      bool
	// endWithoutAssemble prevents a duplicate assembled report.
	endWithoutAssemble bool
	history            []api.Message
	assistantMsg       api.Message
}

func (l turnCloseout) maxCitationGroundingRetries() int {
	if l.PromptLoop != nil && l.Deps.MaxCloseoutCitationGroundingRetries > 0 {
		return l.Deps.MaxCloseoutCitationGroundingRetries
	}
	return limits.DefaultCitationGroundingRetries
}

func (l turnCloseout) handleAcceptedCloseoutReport(
	ctx context.Context,
	sess *api.Session,
	sessionID, userPrompt, surfaceID string,
	st *promptLoopTurnState,
	history []api.Message,
	assistantMsg api.Message,
	read guidance.CloseoutRead,
) (closeoutCommitOutcome, error) {
	var out closeoutCommitOutcome
	if l.PromptLoop == nil {
		return out, fmt.Errorf("closeout grounding not configured")
	}
	report := read.Report
	if l.Deps.CloseoutStallState != nil {
		if retained := l.Deps.CloseoutStallState(ctx, sessionID); retained.Active {
			if pinned := guidance.UsableCloseoutSynthesis(retained.Drafted); pinned != "" {
				report.Synthesis = pinned
			}
			if original, ok := guidance.ParseCoordinatorCompletionReport(retained.Drafted); ok {
				report = guidance.PinCloseoutReport(original, report, guidance.RepairsReportDocument(retained.ForcedBy))
			}
		}
	}
	observation, err := l.observeCloseoutReport(ctx, sess, history, surfaceID, report, read.Unread, st.turnTools, completionReportBinding(st).PhaseDeliversRunReport)
	if err != nil {
		return out, err
	}
	if l.Deps.EvaluateCloseoutBlock == nil {
		return out, fmt.Errorf("closeout OAR not configured")
	}
	decision, err := l.Deps.EvaluateCloseoutBlock(ctx, sess, observation.facts)
	if err != nil {
		return out, err
	}
	if decision != nil && decision.Code != "" {
		draft, err := guidance.MarshalCoordinatorCompletionReport(report)
		if err != nil {
			return out, err
		}
		out, err := l.rejectCloseoutGrounding(ctx, sessionID, st, history, assistantMsg.ID, decision, draft, read.Unread)
		if err != nil {
			return out, err
		}
		if observation.facts.Rejection.RejectObservation == "closeout_no_new_evidence" && decision.Code == guidance.CloseoutNoNewEvidenceCode(surfaceID) {
			out.retry = false
			out.exhausted = false
			out.endWithoutAssemble = true
			st.turnEndedGuidanceReject = true
		}
		return out, nil
	}

	// Missing references are attached from host evidence without another model turn.
	if observation.citations.CitationsRequired {
		code := strings.TrimSpace(observation.citations.Code)
		if code == "" {
			code = guidance.CloseoutCitationsRequiredCode(surfaceID)
		}
		draft, err := guidance.MarshalCoordinatorCompletionReport(report)
		if err != nil {
			return out, err
		}
		return l.emitAssembledCloseout(ctx, sess, sessionID, userPrompt, surfaceID, draft, []string{code}, st, history)
	}

	raw, err := guidance.MarshalCoordinatorCompletionReport(report)
	if err != nil {
		return out, err
	}
	assistantMsg.Content = raw
	assistantMsg.Kind = api.MessageKindCompletionReport
	binding := completionReportBinding(st)
	assistantMsg.WorkflowRunID = binding.RunID
	assistantMsg.CompletionReport = completionReportMeta(surfaceID, binding, report)
	report = guidance.BindCloseoutPresentArtifacts(report, history)
	if len(report.ArtifactIDs) > 0 {
		assistantMsg.ArtifactIDs = append([]string(nil), report.ArtifactIDs...)
	}
	for i := range history {
		if history[i].ID == assistantMsg.ID {
			history[i].Content = raw
			history[i].ArtifactIDs = append([]string(nil), assistantMsg.ArtifactIDs...)
			history[i].CompletionReport = assistantMsg.CompletionReport
			break
		}
	}
	committed, err := l.commitGuardedAssistantTurn(ctx, sess, sessionID, history, userPrompt, surfaceID, assistantMsg)
	if err != nil {
		return out, err
	}
	for i := range history {
		if history[i].ID == assistantMsg.ID {
			history[i] = committed
			break
		}
	}
	if err := turnNudges(l).closeCoordinatorDraftSlot(ctx, sessionID, st, committed.ID); err != nil {
		return out, err
	}
	l.completeCloseout(ctx, sessionID, st)
	if committed.Grounding != nil && committed.Grounding.Traced {
		l.maybeNotifyGroundedSynthesisAccepted(ctx, sess, sessionID, surfaceID, history, workersIdleForSynthesis(l.PromptLoop, ctx, sess))
	}
	out.committed = true
	out.history = history
	out.assistantMsg = committed
	return out, nil
}

func (l turnCloseout) rejectCloseoutGrounding(
	ctx context.Context,
	sessionID string,
	st *promptLoopTurnState,
	history []api.Message,
	assistantMessageID string,
	decision *oar.Decision,
	draftedContent string,
	unread []jsonshape.Issue,
) (closeoutCommitOutcome, error) {
	out := closeoutCommitOutcome{draftedContent: draftedContent}
	code, hintData := decision.Code, decision.Data
	if c := strings.TrimSpace(code); c != "" {
		st.closeoutRetry.codes = append(st.closeoutRetry.codes, c)
	}
	retryEligible := l.Deps.HintConfig != nil && l.Deps.HintConfig.IsInSessionRetry(code)
	key := guidance.GroundingOffenderKey(code, hintData)
	stuck := key != "" && key == st.closeoutRetry.prevKey && !guidance.GroundingRetryBypassesStuckDetection(code)
	budget := l.closeoutRetryBudget(ctx, sessionID, st, code, draftedContent)
	// Retries retain their count and report across runs.
	if l.Deps.NoteCloseoutGroundingReject != nil {
		l.Deps.NoteCloseoutGroundingReject(ctx, sessionID, code, key, draftedContent, unread)
	}
	exhausted := !retryEligible || stuck || budget.exhausted()
	renderDecision := *decision
	renderDecision.Data = budget.hintData(hintData, unread)
	decision = &renderDecision
	nudge, err := guard.FormatCloseoutGroundingReject(
		ctx,
		l.Deps.RejectFmt,
		l.hostKickRenderer(ctx),
		anchor.InformRenderFor(ctx, budget.kick, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}),
		budget.attempt,
		budget.displayMax(),
		decision,
		guidance.UsableCloseoutSynthesis(draftedContent),
	)
	if err != nil {
		return out, err
	}
	history, err = toolInvocations(l).retractRejectedAssistantTurn(ctx, sessionID, history, assistantMessageID, closeoutDraftSlotID(st, assistantMessageID), guidance.NewRefusal(code, nudge))
	if err != nil {
		return out, err
	}
	history, err = turnNudges(l).appendHostNudge(ctx, sessionID, history, HostNudge{Content: nudge, SignalID: string(budget.kick)}, "", st)
	if err != nil {
		return out, err
	}
	st.closeoutRetry.prevKey = key
	st.lastAssistantID = ""
	st.lastAssistantContent = ""
	if exhausted {
		st.turnEndedGuidanceReject = true
		out.exhausted = true
		out.history = history
		return out, nil
	}
	out.retry = true
	out.history = history
	return out, nil
}

// Rendering failures retain the host marker for the requested kick.
func (l turnCloseout) hostKickRenderer(ctx context.Context) guard.HostKickRenderer {
	if l.PromptLoop == nil || l.Deps.RenderHostKick == nil {
		return nil
	}
	render := l.Deps.RenderHostKick
	return func(kickID string, data map[string]any) string {
		kick, err := render(ctx, kickID, data)
		if err != nil || strings.TrimSpace(kick) == "" {
			return guard.HostKickMarker(kickID)
		}
		return strings.TrimSpace(kick)
	}
}

func closeoutDraftSlotID(st *promptLoopTurnState, assistantMessageID string) string {
	if st != nil && st.usesCoordinatorDraftSlot(assistantMessageID) {
		return st.draftSlotID
	}
	return ""
}

func (l turnCloseout) pinnedCloseoutSynthesis(ctx context.Context, sessionID string) string {
	if l.PromptLoop == nil || l.Deps.CloseoutStallState == nil {
		return ""
	}
	retained := l.Deps.CloseoutStallState(ctx, sessionID)
	if !retained.Active {
		return ""
	}
	return guidance.UsableCloseoutSynthesis(retained.Drafted)
}

// repairsReportDocument reports whether the session's closeout is in a report
// document repair: a refusal of its fields is retained and not yet answered.
func (l turnCloseout) repairsReportDocument(ctx context.Context, sessionID string) bool {
	if l.PromptLoop == nil || l.Deps.CloseoutStallState == nil {
		return false
	}
	retained := l.Deps.CloseoutStallState(ctx, sessionID)
	return retained.Active && guidance.RepairsReportDocument(retained.ForcedBy)
}

func closeoutHasCitationIssues(gc *oar.GuardContext) bool {
	if gc == nil {
		return false
	}
	return gc.Rejection.RejectObservation == "citations_required" || len(gc.Grounding.UnobservedCitedHandles) > 0 || len(gc.Grounding.UnobservedCitedURLs) > 0 || gc.Grounding.CitationUnverifiable
}

func (l turnCloseout) completeCloseout(ctx context.Context, sessionID string, st *promptLoopTurnState) {
	if st != nil {
		st.closeoutRetry = closeoutRetryState{}
	}
	if l.Deps.ClearCloseoutStall != nil {
		l.Deps.ClearCloseoutStall(ctx, sessionID)
	}
}
