package promptloop

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

var errCloseoutAssemblerUnavailable = errors.New("closeout assembler unavailable")

// resumeWork ends report pinning before any selected tool can produce an outcome.
func (l *turnCloseout) resumeWork(ctx context.Context, sessionID string, st *promptLoopTurnState) {
	if l.Deps.ResumeCloseoutWork != nil {
		l.Deps.ResumeCloseoutWork(ctx, sessionID)
	}
	st.closeoutRetry.prevKey = ""
	st.closeoutRetry.codes = nil
}

// storedReportIssues collects unread draft members and failed document checks.
func (l *turnCloseout) storedReportIssues(ctx context.Context, sessionID string, report guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error) {
	var issues []guidance.ReportDocumentIssue
	var unread []jsonshape.Issue
	if l.Deps.CloseoutStallState != nil {
		unread = l.Deps.CloseoutStallState(ctx, sessionID).Unread
	}
	if issue, ok := guidance.ReportFenceUnreadable(unread); ok {
		issues = append(issues, issue)
	}
	if l.Deps.CheckRunReportDocument != nil {
		checked, err := l.Deps.CheckRunReportDocument(ctx, sessionID, report)
		if err != nil {
			return nil, err
		}
		issues = append(issues, checked...)
	}
	return issues, nil
}

func (l *turnCloseout) emitAssembledCloseout(
	ctx context.Context,
	sess *api.Session,
	sessionID, userPrompt, surfaceID string,
	draft guidance.CloseoutDraft,
	forcedBy []string,
	st *promptLoopTurnState,
	history []api.Message,
) (closeoutCommitOutcome, error) {
	var out closeoutCommitOutcome
	if l == nil || l.Deps.AssembleLedgerCloseout == nil {
		return out, errCloseoutAssemblerUnavailable
	}
	retryCount := min(st.closeoutRetry.attempt, l.maxCitationGroundingRetries())
	report, grounding := l.Deps.AssembleLedgerCloseout(ctx, sessionID, surfaceID, forcedBy, draft, retryCount)
	if strings.TrimSpace(report.Synthesis) == "" {
		return out, nil
	}
	// Embeds the host cannot present leave the body: artifact ids become
	// attachment references, unobserved sources are dropped.
	observed, err := l.observedURLs(ctx, sess, history, citationmd.Parse(report.Synthesis, citationmd.Options{}))
	if err != nil {
		return out, err
	}
	if clean, embedIDs := guidance.StripCloseoutImageEmbeds(report.Synthesis, observed); clean != report.Synthesis {
		report.Synthesis = clean
		if len(report.ArtifactIDs) == 0 {
			report.ArtifactIDs = embedIDs
		}
	}
	report = guidance.BindCloseoutPresentArtifacts(report, history)

	msgID := ""
	if st != nil && st.draftSlotAppended {
		msgID = st.draftSlotID
	}
	appendNew := strings.TrimSpace(msgID) == ""
	if appendNew {
		msgID = uuid.NewString()
	}
	msg := newProvisionalAssistantMessage(api.Message{
		ID:          msgID,
		Origin:      api.MessageOriginHost,
		Content:     report.Synthesis,
		Grounding:   grounding,
		DraftStatus: api.DraftStatusLive,
		ArtifactIDs: append([]string(nil), report.ArtifactIDs...),
	})
	if grounding != nil {
		msg.Kind = api.MessageKindCompletionReport
		binding := completionReportBinding(st)
		msg.WorkflowRunID = binding.RunID
		msg.CompletionReport = completionReportMeta(
			surfaceID, binding, report,
		)
		// The host stores what the coordinator could not repair, and says so.
		if msg.CompletionReport.Scope == api.CompletionReportScopeRun {
			issues, err := l.storedReportIssues(ctx, sessionID, report)
			if err != nil {
				return out, err
			}
			msg.CompletionReport.Defects = guidance.ReportDocumentDefects(issues)
		}
	} else {
		msg.Kind = api.MessageKindDraft
	}
	if appendNew {
		if l.Projection.Deps.AppendMessages != nil {
			var err error
			msg, err = l.Projection.appendMessage(ctx, sessionID, msg)
			if err != nil {
				return out, err
			}
		}
		history = append(history, msg)
	} else {
		if l.Projection.Deps.UpdateMessage != nil {
			if err := l.Projection.Deps.UpdateMessage(ctx, sessionID, msgID, msg); err != nil {
				return out, err
			}
		}
		found := false
		for i := range history {
			if history[i].ID == msgID {
				msg.Ord = history[i].Ord
				msg.Seq = history[i].Seq
				history[i] = msg
				found = true
				break
			}
		}
		if !found {
			if st != nil && st.usesCoordinatorDraftSlot(msgID) {
				msg.Ord = st.draftSlotOrd
				msg.Seq = st.draftSlotSeq
			}
			history = append(history, msg)
		}
	}

	if st != nil && !tools.CallReach(ctx).Known() {
		ctx = tools.WithCallReach(ctx, st.requestReach())
	}
	committed, err := l.Projection.commitGuardedAssistantTurn(ctx, sess, sessionID, history, userPrompt, surfaceID, msg)
	if err != nil {
		return out, err
	}
	for i := range history {
		if history[i].ID == committed.ID {
			history[i] = committed
			break
		}
	}
	history, err = l.Nudges.retireToolFreeDraftRecovery(ctx, sessionID, history, committed.ID)
	if err != nil {
		return out, err
	}
	if st != nil {
		if err := l.Nudges.closeCoordinatorDraftSlot(ctx, sessionID, st, committed.ID); err != nil {
			return out, err
		}
		st.lastAssistantID = committed.ID
		st.lastAssistantContent = committed.Content
		st.turnEndedGuidanceReject = false
	}
	l.completeCloseout(ctx, sessionID, st)
	out.committed = true
	out.history = history
	out.assistantMsg = committed
	return out, nil
}
