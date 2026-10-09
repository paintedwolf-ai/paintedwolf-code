package workflow

import (
	"context"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReportAvailable is the host authorization shared by downloads and run UI.
func (m *RunManager) ReportAvailable(ctx context.Context, runID string) (bool, error) {
	run, err := m.loadRun(ctx, runID)
	if err != nil {
		return false, err
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return false, err
	}
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return false, err
	}
	return reportAvailable(run, manifest, vars), nil
}

// A report the host stored without accepting it stays downloadable: the
// document states what it is missing.
func reportAvailable(run *api.WorkflowRun, manifest workflowdef.Manifest, vars map[string]any) bool {
	if run != nil && manifest.ReportEnabled() && (run.PauseReason == ReviewBlockedReason || run.Status == api.WorkflowRunStatusCanceled) {
		repair, err := CurrentReviewRepair(vars, run.CurrentPhase)
		if err == nil && repair != nil && repair.State == "blocked" && repair.Snapshot != nil {
			return true
		}
	}
	return run != nil && IsTerminal(run.Status) && manifest.ReportEnabled() &&
		(gateSatisfiedInVars(vars, "topology_report_delivered") || reportNotAcceptedRun(run))
}
