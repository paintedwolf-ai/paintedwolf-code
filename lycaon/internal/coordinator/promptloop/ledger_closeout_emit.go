package promptloop

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

var errCloseoutAssemblerUnavailable = errors.New("closeout assembler unavailable")

// The tool-turn fuse applies only to coordinator closeout surfaces.
func (l *PromptLoop) closeoutFuseTripped(ctx context.Context, sess *api.Session, sessionID, surfaceID string) bool {
	if l == nil || l.Deps.NoteCoordinatorToolTurn == nil {
		return false
	}
	if sess == nil || sess.IsWorkerChild() || !surface.SurfaceDeliversReport(surfaceID) {
		return false
	}
	return l.Deps.NoteCoordinatorToolTurn(ctx, sessionID)
}

func (l *PromptLoop) emitStalledCloseout(
	ctx context.Context,
	sess *api.Session,
	sessionID, userPrompt, surfaceID string,
	st *promptLoopTurnState,
	history []api.Message,
) (closeoutCommitOutcome, error) {
	var retained guidance.RetainedCloseout
	if l.Deps.CloseoutStallState != nil {
		retained = l.Deps.CloseoutStallState(ctx, sessionID)
	}
	return l.emitAssembledCloseout(ctx, sess, sessionID, userPrompt, surfaceID, retained.Drafted, retained.ForcedBy, st, history)
}

// storedReportIssues are the requirements a run report the host stores
// without accepting still fails: members of the draft the report could not
// read, from the draft itself or, once retained as an envelope, from its latest
// refusal, and then the run's document checks.
func (l *PromptLoop) storedReportIssues(ctx context.Context, sessionID, draftedContent string, report guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error) {
	var issues []guidance.ReportDocumentIssue
	read, _ := guidance.ReadCloseoutReport(draftedContent, "")
	unread := read.Unread
	if len(unread) == 0 && l.Deps.CloseoutStallState != nil {
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

func (l *PromptLoop) emitAssembledCloseout(
	ctx context.Context,
	sess *api.Session,
	sessionID, userPrompt, surfaceID, draftedContent string,
	forcedBy []string,
	st *promptLoopTurnState,
	history []api.Message,
) (closeoutCommitOutcome, error) {
	var out closeoutCommitOutcome
	if l == nil || l.Deps.AssembleLedgerCloseout == nil {
		return out, errCloseoutAssemblerUnavailable
	}
	retryCount := min(st.closeoutRetry.attempt, l.maxCitationGroundingRetries())
	report, grounding := l.Deps.AssembleLedgerCloseout(ctx, sessionID, surfaceID, forcedBy, draftedContent, retryCount)
	if strings.TrimSpace(report.Synthesis) == "" {
		return out, nil
	}
	// Invalid artifact embeds become attachment references.
	if clean, embedIDs := guidance.StripCloseoutMarkdownArtifactEmbeds(report.Synthesis); len(embedIDs) > 0 {
		report.Synthesis = clean
		if len(report.ArtifactIDs) == 0 {
			report.ArtifactIDs = embedIDs
		}
	}
	report = guidance.BindCloseoutPresentArtifacts(report, history)
	raw, err := guidance.MarshalCoordinatorCompletionReport(report)
	if err != nil {
		return out, err
	}

	msgID := ""
	if st != nil {
		msgID = st.draftSlotID
	}
	appendNew := strings.TrimSpace(msgID) == ""
	if appendNew {
		msgID = uuid.NewString()
	}
	msg := newProvisionalAssistantMessage(api.Message{
		ID:          msgID,
		Origin:      api.MessageOriginHost,
		Content:     raw,
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
			issues, err := l.storedReportIssues(ctx, sessionID, draftedContent, report)
			if err != nil {
				return out, err
			}
			msg.CompletionReport.Defects = guidance.ReportDocumentDefects(issues)
		}
	} else {
		msg.Kind = api.MessageKindDraft
	}
	// The wire carries narrative; loop history retains the structured report.
	wire := wireCopy(msg, closeoutWireContent(surfaceID))
	if appendNew {
		if l.Deps.AppendMessages != nil {
			if err := l.Deps.AppendMessages(ctx, sessionID, wire); err != nil {
				return out, err
			}
		}
		history = append(history, msg)
	} else {
		if l.Deps.UpdateMessage != nil {
			if err := l.Deps.UpdateMessage(ctx, sessionID, msgID, wire); err != nil {
				return out, err
			}
		}
		found := false
		for i := range history {
			if history[i].ID == msgID {
				history[i] = msg
				found = true
				break
			}
		}
		if !found {
			history = append(history, msg)
		}
	}

	committed, err := l.commitGuardedAssistantTurn(ctx, sess, sessionID, history, userPrompt, surfaceID, msg)
	if err != nil {
		return out, err
	}
	for i := range history {
		if history[i].ID == committed.ID {
			history[i] = committed
			break
		}
	}
	if st != nil {
		if err := l.closeCoordinatorDraftSlot(ctx, sessionID, st, committed.ID); err != nil {
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
