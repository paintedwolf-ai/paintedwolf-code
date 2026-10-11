package contractfixture

import (
	"context"

	"github.com/lycaon/lycaon/internal/cost"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type ProjectReportCountingTracker struct {
	cost.CostTracker
	SummaryCalls int
	ReportCalls  int
}

func (t *ProjectReportCountingTracker) Summary(ctx context.Context, scope wire.CostScope, sessionID, projectID string) (wire.CostSummary, error) {
	t.SummaryCalls++
	return t.CostTracker.Summary(ctx, scope, sessionID, projectID)
}

func (t *ProjectReportCountingTracker) ProjectReport(ctx context.Context, projectID string, query cost.ReportQuery) (wire.ProjectCostReport, error) {
	t.ReportCalls++
	return t.CostTracker.ProjectReport(ctx, projectID, query)
}
