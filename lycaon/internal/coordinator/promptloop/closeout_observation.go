package promptloop

import (
	"context"
	"maps"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

type closeoutReportObservation struct {
	facts     *oar.GuardContext
	citations guard.CloseoutGroundingVerdict
}

// Order: document fields, image embeds, the run's document checks, then references:
// a refused repair, damaged syntax, every supplied reference, the citation-required
// decision, and last a repeat of the previous closeout.
func (l *turnCloseout) observeCloseoutReport(ctx context.Context, sess *api.Session, history []api.Message, surfaceID string, read guidance.CloseoutRead, deliversRunReport bool) (closeoutReportObservation, error) {
	observation := closeoutReportObservation{facts: oar.NewGuardContext()}
	gc := observation.facts
	gc.Session.Surface = strings.TrimSpace(surfaceID)
	draft := read.Draft
	if issue, ok := guidance.ReportFenceUnreadable(read.Unread); ok {
		putReportFieldRefusals(gc, []guidance.ReportDocumentIssue{issue})
		return observation, nil
	}
	if embeds := guidance.CloseoutMarkdownArtifactEmbedIDs(draft.Parsed); len(embeds) > 0 {
		gc.Rejection.RejectObservation = "present_markdown_embed"
		gc.PutRejectData(guidance.PresentMarkdownEmbedCode, guidance.OffenderHintData(embeds))
		return observation, nil
	}
	if deliversRunReport && l.Deps.CheckRunReportDocument != nil {
		issues, err := l.Deps.CheckRunReportDocument(ctx, sess.ID, draft.Report())
		if err != nil {
			return observation, err
		}
		if len(issues) > 0 {
			putReportFieldRefusals(gc, issues)
			return observation, nil
		}
	}
	verdict, err := guard.EvaluateCoordinatorCloseoutGrounding(ctx, l.Deps.EvidenceLedger, sess, history, surfaceID, draft, l.Context.citationRoots(ctx, sess))
	if err != nil {
		return observation, err
	}
	observation.citations = verdict
	if len(read.RepairDefects) > 0 {
		putCitationDefect(gc, "citation_repair_invalid", guidance.CitationRepairInvalidCode, verdict.HintData, guidance.CitationRepairRefusalData(read.RepairDefects))
		return observation, nil
	}
	if len(draft.Issues) > 0 {
		putCitationDefect(gc, "citation_syntax_invalid", guidance.CitationSyntaxInvalidCode, verdict.HintData, guidance.CitationSyntaxRefusalData(draft.Issues))
		return observation, nil
	}
	guard.PublishCloseoutGrounding(gc, surfaceID, draft, verdict)
	if closeoutHasCitationIssues(gc) {
		return observation, nil
	}
	if hit, offenders := guidance.CloseoutCitationsSubsetOfPrior(history, verdict.Grounding); hit {
		gc.Rejection.RejectObservation = "closeout_no_new_evidence"
		gc.PutRejectData(guidance.CloseoutNoNewEvidenceCode(surfaceID), guidance.OffenderHintData(offenders))
	}
	return observation, nil
}

// putCitationDefect refuses on its own code, with the repair kick's choices.
func putCitationDefect(gc *oar.GuardContext, rejectObservation, code string, hints, refusal map[string]any) {
	gc.Rejection.RejectObservation = rejectObservation
	data := make(map[string]any, len(hints)+len(refusal))
	maps.Copy(data, hints)
	maps.Copy(data, refusal)
	gc.PutRejectData(code, data)
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
	listed := make([]map[string]any, 0, len(issues))
	for _, each := range issues {
		listed = append(listed, map[string]any{"reason": each.Reason, "offenders": each.Offenders})
	}
	data["document_issues"] = listed
	gc.PutRejectData(issue.Code, data)
}
