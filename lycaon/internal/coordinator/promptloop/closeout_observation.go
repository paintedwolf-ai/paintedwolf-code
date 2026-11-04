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

// Document defects precede citation repair, which retains the document fields.
func (l *turnCloseout) observeCloseoutReport(ctx context.Context, sess *api.Session, history []api.Message, surfaceID string, report guidance.CoordinatorCompletionReport, unread []jsonshape.Issue, turnTools []string, deliversRunReport bool) (closeoutReportObservation, error) {
	observation := closeoutReportObservation{facts: oar.NewGuardContext()}
	gc := observation.facts
	gc.Session.Surface = strings.TrimSpace(surfaceID)
	var issues []guidance.ReportDocumentIssue
	if issue, ok := guidance.ReportFenceUnreadable(unread); ok {
		putReportFieldRefusals(gc, []guidance.ReportDocumentIssue{issue})
		return observation, nil
	}
	if embeds := guidance.CloseoutMarkdownArtifactEmbedIDs(report.Synthesis); len(embeds) > 0 {
		gc.Rejection.RejectObservation = guidance.PresentationObservation(guidance.PresentMarkdownEmbedCode)
		gc.PutRejectData(guidance.PresentMarkdownEmbedCode, guidance.OffenderHintData(embeds))
		return observation, nil
	}
	unobserved, err := l.unobservedImageSources(ctx, sess, history, report.Synthesis)
	if err != nil {
		return observation, err
	}
	if len(unobserved) > 0 {
		gc.Rejection.RejectObservation = guidance.PresentationObservation(guidance.PresentImageUnobservedCode)
		gc.PutRejectData(guidance.PresentImageUnobservedCode, guidance.OffenderHintData(unobserved))
		return observation, nil
	}
	if deliversRunReport && l.Deps.CheckRunReportDocument != nil {
		checked, err := l.Deps.CheckRunReportDocument(ctx, sess.ID, report)
		if err != nil {
			return observation, err
		}
		issues = append(issues, checked...)
	}
	if len(issues) > 0 {
		putReportFieldRefusals(gc, issues)
		return observation, nil
	}
	if !guard.CoordinatorEvidenceOptional(history, turnTools) {
		if l.Deps.EvidenceLedger == nil {
			return observation, fmt.Errorf("closeout grounding not configured")
		}
		verdict, err := guard.ObserveCoordinatorCloseoutGrounding(ctx, l.Deps.EvidenceLedger, sess, history, surfaceID, report, turnTools, l.Context.citationRoots(ctx, sess), gc)
		if err != nil {
			return observation, err
		}
		observation.citations = verdict
		if closeoutHasCitationIssues(gc) {
			return observation, nil
		}
	}
	if hit, offenders := guidance.CloseoutCitationsSubsetOfPrior(history, report); hit {
		gc.Rejection.RejectObservation = "closeout_no_new_evidence"
		gc.PutRejectData(guidance.CloseoutNoNewEvidenceCode(surfaceID), guidance.OffenderHintData(offenders))
	}
	return observation, nil
}

// observedURLs is the evidence union's URL check, read only when the body
// embeds a source the union would have to vouch for. Nil vouches for nothing.
func (l *turnCloseout) observedURLs(ctx context.Context, sess *api.Session, history []api.Message, synthesis string) (func(string) bool, error) {
	if l.Deps.EvidenceLedger == nil || len(guidance.CloseoutUnobservedImageSources(synthesis, nil)) == 0 {
		return nil, nil
	}
	ev, err := guidance.UnionCloseoutEvidence(ctx, l.Deps.EvidenceLedger, sess.ID, history)
	if err != nil {
		return nil, err
	}
	return ev.URLSeen, nil
}

// unobservedImageSources lists the image sources the body embeds that no
// artifact and no observed URL vouch for.
func (l *turnCloseout) unobservedImageSources(ctx context.Context, sess *api.Session, history []api.Message, synthesis string) ([]string, error) {
	observed, err := l.observedURLs(ctx, sess, history, synthesis)
	if err != nil {
		return nil, err
	}
	return guidance.CloseoutUnobservedImageSources(synthesis, observed), nil
}

// The first defect selects policy; every defect accompanies the repair.
func putReportFieldRefusals(gc *oar.GuardContext, issues []guidance.ReportDocumentIssue) {
	issue := issues[0]
	gc.Rejection.RejectObservation = guidance.ReportDocumentObservation(issue.Code)
	data := guidance.OffenderHintData(issue.Offenders)
	// The issue samples its subjects; the refusal counts every one of them.
	if extra := issue.Count - len(issue.Offenders); extra > 0 {
		data["offender_count"] = issue.Count
		data["offenders_omitted"] = data["offenders_omitted"].(int) + extra
	}
	data["rejection_reason"] = issue.Reason
	data["document_issues"] = issues
	gc.PutRejectData(issue.Code, data)
}
