package promptloop

import (
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// CompletionReportBinding is the workflow snapshot that produced a completion.
type CompletionReportBinding struct {
	// RunID is the active workflow run, empty for unbound or worker replies.
	RunID string
	Phase string
	// PhaseDeliversRunReport marks the phase the manifest gates on
	// topology_report_delivered — the one declared to produce the run's report.
	PhaseDeliversRunReport bool
	// CloseoutRetries is the workflow-configured retry limit for this phase,
	// or 0 to use system defaults.
	CloseoutRetries int
}

// completionReportMeta states what a report covers: the session outside a run,
// the run in its declared report phase, and otherwise just that phase. What the
// report concluded travels on the same record, since the message body may be
// prose or an envelope but the record's shape never varies.
func completionReportMeta(
	surfaceID string, binding CompletionReportBinding, report guidance.CoordinatorCompletionReport,
) *api.CompletionReportMeta {
	meta := &api.CompletionReportMeta{
		Scope:     api.CompletionReportScopeSession,
		SurfaceID: strings.TrimSpace(surfaceID),
	}
	if strings.TrimSpace(binding.RunID) == "" {
		return meta
	}
	meta.Phase = strings.TrimSpace(binding.Phase)
	if binding.PhaseDeliversRunReport {
		meta.Scope = api.CompletionReportScopeRun
		meta.Headline = strings.TrimSpace(report.Headline)
		meta.Summary = strings.TrimSpace(report.Summary)
		meta.Findings = reportFindingsMeta(report.Findings)
		meta.Limits = append([]string(nil), report.Limits...)
		meta.Ask = reportAskMeta(report.Ask)
		meta.Rating = reportRatingMeta(report.Rating)
		meta.SetAsides = reportSetAsidesMeta(report.SetAsides)
		return meta
	}
	meta.Scope = api.CompletionReportScopePhase
	return meta
}

// reportFindingsMeta projects the authored findings onto the wire record.
func reportFindingsMeta(findings []guidance.CoordinatorFinding) []api.CompletionReportFinding {
	if len(findings) == 0 {
		return nil
	}
	out := make([]api.CompletionReportFinding, 0, len(findings))
	for _, f := range findings {
		row := api.CompletionReportFinding{
			ID:           f.ID,
			Title:        f.Title,
			Severity:     f.Severity,
			Status:       f.Status,
			Impact:       f.Impact,
			Action:       f.Action,
			ScanGroupIds: append([]string(nil), f.ScanGroupIDs...),
		}
		// A host-stored report can carry a value the document check refused;
		// its defects record it and the wire keeps its enum.
		if d := api.CompletionReportFindingDisposition(f.Disposition); slices.Contains(api.AllCompletionReportFindingDispositions(), d) {
			row.Disposition = d
		}
		if len(f.Answers) > 0 {
			row.Answers = make(map[string]string, len(f.Answers))
			for k, v := range f.Answers {
				row.Answers[k] = v
			}
		}
		for _, w := range f.Where {
			row.Where = append(row.Where, api.CompletionReportFindingLocation{
				Handle: w.Evidence,
				Path:   w.Path,
				Line:   w.Line,
			})
		}
		out = append(out, row)
	}
	return out
}

// reportRatingMeta projects the review's call onto the wire record.
func reportRatingMeta(rating *guidance.CoordinatorRating) *api.CompletionReportRating {
	if rating == nil || strings.TrimSpace(rating.Level) == "" {
		return nil
	}
	return &api.CompletionReportRating{Level: strings.TrimSpace(rating.Level), Why: strings.TrimSpace(rating.Why)}
}

// reportAskMeta drops an ask whose effort is off the enum; only a host-stored
// report reaches here with one, and its defects name it.
func reportAskMeta(ask *guidance.CoordinatorAsk) *api.CompletionReportAsk {
	if ask == nil {
		return nil
	}
	effort := api.CompletionReportAskEffort(ask.Effort)
	if !slices.Contains(api.AllCompletionReportAskEfforts(), effort) {
		return nil
	}
	return &api.CompletionReportAsk{Do: ask.Do, Effort: effort, Why: ask.Why}
}

func reportSetAsidesMeta(setAsides []guidance.CoordinatorSetAside) []api.CompletionReportSetAside {
	if len(setAsides) == 0 {
		return nil
	}
	out := make([]api.CompletionReportSetAside, 0, len(setAsides))
	for _, sa := range setAsides {
		out = append(out, api.CompletionReportSetAside{
			ScanGroupIds: append([]string(nil), sa.ScanGroupIDs...),
			Scanner:      sa.Scanner,
			Paths:        append([]string(nil), sa.Paths...),
			Reason:       sa.Reason,
		})
	}
	return out
}

// completionReportBinding preserves the workflow snapshot used for this response.
func completionReportBinding(st *promptLoopTurnState) CompletionReportBinding {
	if st == nil {
		return CompletionReportBinding{}
	}
	frame := st.coordinatorFrame
	return CompletionReportBinding{
		RunID:                  frame.RunContext.RunID,
		Phase:                  frame.RunContext.CurrentPhase,
		PhaseDeliversRunReport: frame.Runtime.ReportDocumentEnabled,
		CloseoutRetries:        frame.Runtime.CloseoutRetries,
	}
}
