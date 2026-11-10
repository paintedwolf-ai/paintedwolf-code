package delegations

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (r *Runtime) DecodeCompleteLeg(ctx context.Context, args map[string]any, tctx tools.ToolContext, validateCoverage func(context.Context, *wire.WorkerTask, *wire.CoverageReview) error) (*wire.WorkerReportAcceptance, error) {
	report, err := workercompletion.DecodeCompleteLegArgs(args)
	if err != nil {
		return nil, err
	}
	task, ok := r.Queue.Lookup(ctx, tctx.Identity.WorkerJobID)
	if !ok {
		return nil, fmt.Errorf("worker job %q unavailable", tctx.Identity.WorkerJobID)
	}
	if report.LegStatus == "complete" && validateCoverage != nil {
		if err := validateCoverage(ctx, task, report.CoverageReview); err != nil {
			return nil, err
		}
	}
	input, err := r.workerReportInput(ctx, task, tctx, report)
	if err != nil {
		return nil, err
	}
	return workercompletion.AcceptWorkerReport(ctx, task.ID, input)
}

func (r *Runtime) workerReportInput(ctx context.Context, task *wire.WorkerTask, tctx tools.ToolContext, report workercompletion.WorkerCompletionReport) (workercompletion.WorkerSummaryEvalInput, error) {
	if r.deps.Storage.Sessions == nil || r.deps.Sessions == nil || r.deps.Sessions.Manager == nil {
		return workercompletion.WorkerSummaryEvalInput{}, fmt.Errorf("worker report services unavailable")
	}
	child, err := r.deps.Storage.Sessions.Get(ctx, tctx.Identity.SessionID)
	if err != nil {
		return workercompletion.WorkerSummaryEvalInput{}, err
	}
	if child == nil {
		return workercompletion.WorkerSummaryEvalInput{}, fmt.Errorf("worker session %q unavailable", tctx.Identity.SessionID)
	}
	if task.ChildSessionID != child.ID || task.ProjectID != child.ProjectID {
		return workercompletion.WorkerSummaryEvalInput{}, fmt.Errorf("worker report identity does not match job %q", task.ID)
	}
	manager := r.deps.Sessions.Manager
	messages, err := manager.Runner.Transcript.GetWorkerJobMessages(ctx, child.ID, task.ID)
	if err != nil {
		return workercompletion.WorkerSummaryEvalInput{}, err
	}
	projectDir, err := manager.Workspace.ActivePath(ctx, child)
	if err != nil {
		return workercompletion.WorkerSummaryEvalInput{}, err
	}
	bounds := manager.Workers.WorkerSummaryFinalizeOpts(ctx, child)
	return workercompletion.WorkerSummaryEvalInput{
		AgentType: task.AgentType, MaxChars: bounds.MaxChars, Report: report,
		ChildSessionID: child.ID, ChildMessages: messages, ProjectDir: projectDir,
		ProjectRoots: manager.Workspace.ProjectRoots(ctx, task.ProjectID), ActiveRootID: child.WorkspaceRootID,
		WorkspaceCheck: bounds.WorkspaceCheck, Ledger: workercloseout.LedgerForMessages(bounds.Ledger, messages),
		Hints: bounds.WorkflowHints, Pipeline: bounds.Pipeline, Reach: tools.CallReach(ctx),
	}, nil
}
