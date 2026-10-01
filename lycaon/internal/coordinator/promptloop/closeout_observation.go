package promptloop

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

type closeoutReportObservation struct {
	facts     *oar.GuardContext
	citations guard.CloseoutGroundingVerdict
}

// A report the host could not fully read is refused first, since the fields
// it left out would decide the rest; then embed and citation issues, then the
// run report's document, then a duplicate report.
func (l *PromptLoop) observeCloseoutReport(ctx context.Context, sess *api.Session, history []api.Message, surfaceID string, report guidance.CoordinatorCompletionReport, unread []jsonshape.Issue, turnTools []string, deliversRunReport bool) (closeoutReportObservation, error) {
	observation := closeoutReportObservation{facts: oar.NewGuardContext()}
	gc := observation.facts
	gc.Surface = strings.TrimSpace(surfaceID)
	if issue, ok := guidance.ReportFenceUnreadable(unread); ok {
		putReportFieldRefusal(gc, issue)
		return observation, nil
	}
	if embeds := guidance.CloseoutMarkdownArtifactEmbedIDs(report.Synthesis); len(embeds) > 0 {
		gc.RejectObservation = "present_markdown_embed"
		gc.PutRejectData(guidance.PresentMarkdownEmbedCode, guidance.OffenderHintData(embeds))
		return observation, nil
	}
	if !guard.CoordinatorEvidenceOptional(history, turnTools) {
		if l.Deps.EvidenceLedger == nil {
			return observation, fmt.Errorf("closeout grounding not configured")
		}
		verdict, err := guard.ObserveCoordinatorCloseoutGrounding(ctx, l.Deps.EvidenceLedger, sess, history, surfaceID, report, turnTools, l.citationRoots(ctx, sess), gc)
		if err != nil {
			return observation, err
		}
		observation.citations = verdict
		if closeoutHasCitationIssues(gc) {
			return observation, nil
		}
	}
	if deliversRunReport && l.Deps.CheckRunReportDocument != nil {
		issues, err := l.Deps.CheckRunReportDocument(ctx, sess.ID, report)
		if err != nil {
			return observation, err
		}
		if len(issues) > 0 {
			putReportFieldRefusal(gc, issues[0])
			return observation, nil
		}
	}
	if hit, offenders := guidance.CloseoutCitationsSubsetOfPrior(history, report); hit {
		gc.RejectObservation = "closeout_no_new_evidence"
		gc.PutRejectData(guidance.CloseoutNoNewEvidenceCode(surfaceID), guidance.OffenderHintData(offenders))
	}
	return observation, nil
}

// putReportFieldRefusal states a refusal of the report's fields for its policy.
func putReportFieldRefusal(gc *oar.GuardContext, issue guidance.ReportDocumentIssue) {
	gc.RejectObservation = guidance.ReportDocumentObservation(issue.Code)
	data := guidance.OffenderHintData(issue.Offenders)
	// The issue samples its subjects; the refusal counts every one of them.
	if extra := issue.Count - len(issue.Offenders); extra > 0 {
		data["offender_count"] = issue.Count
		data["offenders_omitted"] = data["offenders_omitted"].(int) + extra
	}
	data["rejection_reason"] = issue.Reason
	gc.PutRejectData(issue.Code, data)
}
