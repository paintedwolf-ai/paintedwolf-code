package workflow

import workflowreview "github.com/lycaon/lycaon/internal/workflow/review"

import (
	"context"

	"github.com/lycaon/lycaon/internal/guidance"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
)

func (m *Reports) CheckRunReportDocument(ctx context.Context, sessionID string, report guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error) {
	if m == nil {
		return nil, nil
	}
	run, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return nil, err
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	inventory, err := workflowreview.LoadRunInventory(ctx, m.Coverage.Inventory, run.ID)
	if err != nil {
		return nil, err
	}
	verdicts := workflowpresentation.ReviewVerdicts(ctx, m.Verdicts, run, manifest)
	return workflowreview.CheckReportDocument(report, workflowreview.ReportDocumentFacts{
		Brief:     manifest.ReportBrief(),
		Claims:    workflowpresentation.ReconcileClaims(verdicts),
		Inventory: inventory,
		SetAsides: workflowpresentation.RunSetAsides(verdicts),
	}), nil
}
