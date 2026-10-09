package lifecycle_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflow "github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func startRun(ctx context.Context, mgr *workflow.RunManager, sessionID, workflowID, version string) (*api.WorkflowRun, error) {
	return mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      workflowID,
		WorkflowVersion: version,
		Request:         "test request",
	})
}

func startPlanRun(ctx context.Context, mgr *workflow.RunManager, sessionID string) (*api.WorkflowRun, error) {
	return startRun(ctx, mgr, sessionID, "plan", "1.0.0")
}

func completePlanIntake(ctx context.Context, mgr *workflow.RunManager, run *api.WorkflowRun) (*api.WorkflowRun, error) {
	return run, nil
}

func completePlanIntakeT(ctx context.Context, t *testing.T, mgr *workflow.RunManager, run *api.WorkflowRun) *api.WorkflowRun {
	t.Helper()
	out, err := completePlanIntake(ctx, mgr, run)
	testutil.FailErr(t, "completePlanIntake", err)
	return out
}

func completePlanResearchAtDepthNone(ctx context.Context, mgr *workflow.RunManager, run *api.WorkflowRun) (*api.WorkflowRun, error) {
	if run == nil || run.CurrentPhase != "research" {
		return run, nil
	}
	return completePlanDepthPhaseAtNone(ctx, mgr, run, "research_depth")
}

func completePlanReviewAtDepthNone(ctx context.Context, mgr *workflow.RunManager, run *api.WorkflowRun) (*api.WorkflowRun, error) {
	if run == nil || run.CurrentPhase != "review" {
		return run, nil
	}
	return completePlanDepthPhaseAtNone(ctx, mgr, run, "review_depth")
}

func completePlanDepthPhaseAtNone(ctx context.Context, mgr *workflow.RunManager, run *api.WorkflowRun, param string) (*api.WorkflowRun, error) {
	manifest, err := mgr.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	vars = runstate.SetHostVar(vars, "params."+param, "none")
	vars = runstate.StampDepthParamSkips(vars, manifest)
	projectDir := ""
	if mgr.Policy.Sessions != nil {
		if sess, getErr := mgr.Policy.Sessions.Get(ctx, run.SessionID); getErr == nil && sess != nil {
			projectDir = sess.WorkspacePath
		}
	}
	if err := mgr.Store.State.UpdateVars(ctx, run, projectDir, vars); err != nil {
		return nil, err
	}
	return mgr.Phases.Advance(ctx, run.ID)
}

func advancePlanThroughExpand(ctx context.Context, mgr *workflow.RunManager, run *api.WorkflowRun) (*api.WorkflowRun, error) {
	if run == nil || run.CurrentPhase != "expand" {
		return run, nil
	}
	return mgr.Phases.Advance(ctx, run.ID)
}

func advancePlanToApprovePhase(ctx context.Context, mgr *workflow.RunManager, run *api.WorkflowRun) (*api.WorkflowRun, error) {
	var err error
	run, err = completePlanIntake(ctx, mgr, run)
	if err != nil {
		return nil, err
	}
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	if err != nil {
		return nil, err
	}
	run, err = advancePlanThroughExpand(ctx, mgr, run)
	if err != nil {
		return nil, err
	}
	run, err = completePlanReviewAtDepthNone(ctx, mgr, run)
	if err != nil {
		return nil, err
	}
	if run, err = mgr.Store.Runs.Get(ctx, run.ID); err != nil {
		return nil, err
	}
	return run, nil
}
