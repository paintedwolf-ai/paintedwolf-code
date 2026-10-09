package workflowadmin

import (
	"github.com/lycaon/lycaon/internal/report"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	wire "github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// reportClaims states each claim as the run's review phases left it.
func reportClaims(claims []workflowpresentation.RunClaim) []report.ReportClaim {
	out := make([]report.ReportClaim, 0, len(claims))
	for _, c := range claims {
		out = append(out, report.ReportClaim{
			ID: c.ID, Title: c.Title, Class: string(c.Class), Status: c.Status, Dropped: c.Dropped,
		})
	}
	return out
}

// reportBrief rates the findings that need attention, preferring the answers
// of a review claim with the same id; unreadable answers rate as unknown. A
// claim the review left open or overturned that no finding carries is rated
// too, as its review answered it or as unknown: an unstated conclusion is not
// a cleared one.
func reportBrief(brief *workflowdef.Brief, findings []assembledFinding, claims, unreported []workflowpresentation.RunClaim) *report.ReportBrief {
	if brief == nil {
		return nil
	}
	out := &report.ReportBrief{Question: brief.Question}
	for _, l := range brief.Levels {
		out.Levels = append(out.Levels, report.ReportLevel{Label: l.Label, Answer: l.Answer, Means: l.Means, Tone: l.Tone})
	}
	for _, d := range brief.Dimensions {
		out.Dimensions = append(out.Dimensions, d.Label)
	}
	reviewed := workflowreview.ClaimAnswers(claims)
	var items []map[string]string
	for i, f := range findings {
		if !f.finding.NeedsAttention() {
			continue
		}
		answers, adjudicated := ratedAnswers(brief, reviewed, f)
		worst, best := brief.LevelRange(answers)
		out.Rated = append(out.Rated, report.ReportRated{
			Number: i + 1, Title: f.finding.Title, Answers: answerLabels(brief, answers),
			Worst: worst, Best: best, Adjudicated: adjudicated,
		})
		items = append(items, answers)
	}
	for _, c := range unreported {
		answers, adjudicated := brief.Rateable(c.Answers), len(c.Answers) > 0
		worst, best := brief.LevelRange(answers)
		title := strings.TrimSpace(c.Title)
		if title == "" {
			title = c.ID
		}
		out.Rated = append(out.Rated, report.ReportRated{
			Title: title, Answers: answerLabels(brief, answers),
			Worst: worst, Best: best, Adjudicated: adjudicated, Unreported: true,
		})
		items = append(items, answers)
	}
	rating := brief.Rate(items)
	out.Worst, out.Best = rating.Worst, rating.Best
	if rating.Decider >= 0 {
		out.Basis = brief.BasisPhrase(items[rating.Decider])
	}
	return out
}

// reportUnreported lists the review claims no finding of the closeout carries.
func reportUnreported(findings []assembledFinding, claims []workflowpresentation.RunClaim) []workflowpresentation.RunClaim {
	ids := make([]string, 0, len(findings))
	for _, f := range findings {
		ids = append(ids, f.finding.ID)
	}
	return workflowreview.UnreportedClaims(ids, claims)
}

func answerLabels(brief *workflowdef.Brief, answers map[string]string) []string {
	out := make([]string, 0, len(brief.Dimensions))
	for _, d := range brief.Dimensions {
		if v, ok := d.Value(answers[d.ID]); ok {
			out = append(out, v.Label)
			continue
		}
		out = append(out, "Unknown")
	}
	return out
}

// reportDefects are the document requirements the stored report failed.
func reportDefects(completion *wire.CompletionReportMeta) []report.ReportDefect {
	if completion == nil || len(completion.Defects) == 0 {
		return nil
	}
	out := make([]report.ReportDefect, 0, len(completion.Defects))
	for _, d := range completion.Defects {
		out = append(out, report.ReportDefect{
			Code: string(d.Code), Reason: d.Reason, Subjects: append([]string(nil), d.Subjects...), Count: d.Count,
		})
	}
	return out
}

// reportAsk is the closeout's one decision for the reader.
func reportAsk(completion *wire.CompletionReportMeta) *report.ReportAsk {
	if completion == nil || completion.Ask == nil || strings.TrimSpace(completion.Ask.Do) == "" {
		return nil
	}
	return &report.ReportAsk{
		Do:     strings.TrimSpace(completion.Ask.Do),
		Effort: string(completion.Ask.Effort),
		Why:    strings.TrimSpace(completion.Ask.Why),
	}
}
