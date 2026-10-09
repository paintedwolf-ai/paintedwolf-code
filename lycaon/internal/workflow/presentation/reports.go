package presentation

import (
	"context"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReportAvailable is the host authorization shared by downloads and run UI.
func (m *Runs) ReportAvailable(ctx context.Context, runID string) (bool, error) {
	run, err := m.Reader.Get(ctx, runID)
	if err != nil {
		return false, err
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return false, err
	}
	vars, err := m.Reader.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return false, err
	}
	return reportAvailable(run, manifest, vars), nil
}

// A report the host stored without accepting it stays downloadable: the
// document states what it is missing.
func reportAvailable(run *api.WorkflowRun, manifest workflowdef.Manifest, vars map[string]any) bool {
	return run != nil && runstate.IsTerminal(run.Status) && manifest.ReportEnabled() &&
		(workflowgates.SatisfiedInVars(vars, "topology_report_delivered") || runstate.ReportNotAcceptedRun(run))
}
