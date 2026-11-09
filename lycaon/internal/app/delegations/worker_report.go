package delegations

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (r *Runtime) DecodeCompleteLeg(ctx context.Context, args map[string]any, tctx tools.ToolContext, validateCoverage func(context.Context, *wire.WorkerTask, *wire.CoverageReview) error) (workertools.CompleteLegRecord, error) {
	record, err := workercompletion.CompleteLegDecoder(ctx, args, tctx)
	if err != nil {
		return record, err
	}
	task, ok := r.Queue.Lookup(ctx, tctx.Identity.WorkerJobID)
	if !ok {
		return record, fmt.Errorf("worker job %q unavailable", tctx.Identity.WorkerJobID)
	}
	report, _ := workercompletion.ReportFromCompleteLegArgs(args)
	if record.LegStatus == "complete" && validateCoverage != nil {
		if err := validateCoverage(ctx, task, report.CoverageReview); err != nil {
			return record, err
		}
	}
	input, err := r.workerReportInput(ctx, task, tctx, report)
	if err != nil {
		return record, err
	}
	evaluated, err := workercompletion.EvaluateWorkerSummary(ctx, input)
	if err != nil {
		return record, err
	}
	if evaluated.HintCode != "" {
		text, err := evaluated.FormatFeedback(ctx, input.Hints)
		if err != nil {
			return record, err
		}
		cause := &toolrejection.ToolReject{Code: evaluated.HintCode, Data: evaluated.HintData}
		return record, guidance.NewRefusal(evaluated.HintCode, text).WithPolicyCopy(evaluated.HintCopy).WithDetails(evaluated.HintData, nil).WithCause(cause)
	}
	record.LegStatus = evaluated.Report.LegStatus
	record.Findings = len(evaluated.Report.Findings)
	record.Acceptance = &wire.WorkerReportAcceptance{WorkerJobID: task.ID, Report: workercompletion.ReportWire(evaluated.Report), Grounding: &evaluated.Grounding}
	return record, nil
}

func (r *Runtime) workerReportInput(ctx context.Context, task *wire.WorkerTask, tctx tools.ToolContext, report workercompletion.WorkerCompletionReport) (workercompletion.WorkerSummaryEvalInput, error) {
	child, err := r.deps.Storage.Sessions.Get(ctx, tctx.Identity.SessionID)
	if err != nil {
		return workercompletion.WorkerSummaryEvalInput{}, err
	}
	if child == nil {
		return workercompletion.WorkerSummaryEvalInput{}, fmt.Errorf("worker session %q unavailable", tctx.Identity.SessionID)
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
