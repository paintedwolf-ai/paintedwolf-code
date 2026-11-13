package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/workerops"
	"strings"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// ChangeReportDeps loads host observations for worker reports.
type ChangeReportDeps struct {
	Messages   func(ctx context.Context, childSessionID string) ([]api.Message, error)
	SourceRuns func(ctx context.Context, childSessionID string) ([]workercompletion.WorkerInvocationReceipt, error)
}

func hostCancellationResult(ctx context.Context, task api.WorkerTask, reason string, deps ChangeReportDeps) api.WorkerResult {
	// A concurrent stop may have already observed and released the workspace.
	if task.Status == api.WorkerStatusCanceled && task.Result != nil && task.Result.ChangeReport != nil {
		return *task.Result
	}
	report := BuildChangeReport(ctx, task, deps)
	return api.WorkerResult{
		Status: "canceled", Response: "canceled",
		Summary: cancellationSummary(task, reason, report), ChangeReport: &report,
	}
}

// Recovered and self-canceled jobs retain the same host observations as explicit stops.
func cancellationResultForJob(ctx context.Context, queue WorkerQueue, jobID string, deps ChangeReportDeps) *api.WorkerResult {
	task, ok := queue.Get(jobID)
	if !ok || task.Status.IsTerminal() {
		return nil
	}
	result := hostCancellationResult(ctx, *task, "", deps)
	return &result
}

// BuildChangeReport compiles a worker's host-observed changes.
func BuildChangeReport(ctx context.Context, task api.WorkerTask, deps ChangeReportDeps) api.WorkerChangeReport {
	var childMessages []api.Message
	if deps.Messages != nil && strings.TrimSpace(task.ChildSessionID) != "" {
		if msgs, err := deps.Messages(ctx, task.ChildSessionID); err == nil {
			childMessages = msgs
		}
	}
	revision, rootDigest := invocation.SourceRevisionForRoot(task.WorkspaceRoot)
	proof := workercompletion.BuildWorkerCompletionProof(
		childMessages,
		session.OverlayWorkspaceChangedPaths(ctx, &task, nil),
		workercompletion.SourceRevision{Revision: revision, RootDigest: rootDigest},
	)
	return api.WorkerChangeReport{
		ChangedPaths:   append([]string(nil), proof.ChangedPaths...),
		WorkspaceDirty: proof.WorkspaceDirty,
		MutationTools:  append([]string(nil), proof.MutationTools...),
		SurveyTools:    append([]string(nil), proof.SurveyTools...),
		ReceiptCount:   proof.ReceiptCount,
	}
}

// SourceEvidenceContext supplies source identity for advisory validation evidence.
type SourceEvidenceContext struct {
	DeclaredCommand func(context.Context, *api.WorkerTask) string
	SourceRevision  func(context.Context, *api.WorkerTask) workercompletion.SourceRevision
}

func (g SourceEvidenceContext) command(ctx context.Context, task *api.WorkerTask) string {
	if g.DeclaredCommand == nil {
		return ""
	}
	return g.DeclaredCommand(ctx, task)
}

func (g SourceEvidenceContext) revision(ctx context.Context, task *api.WorkerTask) workercompletion.SourceRevision {
	if g.SourceRevision == nil {
		return workercompletion.SourceRevision{}
	}
	return g.SourceRevision(ctx, task)
}

// CancelToolDeps wires the coordinator worker_cancel tool.
type CancelToolDeps struct {
	Cancel *CancelService
	// Workers resolves the worker a call names and what stopping it means.
	Workers *workerops.Service
}

// RegisterWorkerCancelTool registers worker_cancel(worker, reason?) for coordinator sessions.
func RegisterWorkerCancelTool(reg *tools.DefaultRegistry, deps CancelToolDeps) error {
	if reg == nil || deps.Cancel == nil {
		return fmt.Errorf("registry and cancel service required")
	}
	return reg.Register("worker_cancel", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		name := stringArgOf(args, "worker")
		if name == "" {
			return "", fmt.Errorf("worker is required")
		}
		reason, _ := args["reason"].(string)
		if deps.Workers == nil {
			return "", fmt.Errorf("worker operations not configured")
		}
		op, err := toolOp("worker_cancel", tctx)
		if err != nil {
			return "", err
		}
		stop, err := deps.Workers.Stop(ctx, coordinatorActor(tctx), op, name, reason)
		if err != nil {
			return "", workerToolReject("worker_cancel", name, "", err)
		}
		result := map[string]any{"worker": stop.Worker}
		if stop.Withdrawn {
			result["withdrawn"] = true
		}
		if stop.JobID != "" {
			captureWorkerSubject(tctx, deps.Cancel.Queue, stop.JobID)
			out, err := deps.Cancel.CancelForSession(ctx, tctx.Identity.SessionID, stop.JobID, reason)
			if err != nil {
				return "", err
			}
			result["status"], result["change_report"], result["result"] = out.Status, out.Report, out.Result
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	})
}

// RegisterAnswerDecisionTool registers worker decision answers.
func RegisterAnswerDecisionTool(reg *tools.DefaultRegistry, deps AnswerDecisionToolDeps) error {
	if reg == nil || deps.Answer == nil {
		return fmt.Errorf("registry and answer decision service required")
	}
	svc := deps.Answer
	return reg.Register("answer_decision", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		name := stringArgOf(args, "worker")
		answer := stringArgOf(args, "answer")
		resolvedBy := stringArgOf(args, "resolved_by")
		if name == "" || answer == "" {
			return "", fmt.Errorf("worker and answer are required")
		}
		if deps.Workers == nil {
			return "", fmt.Errorf("worker operations not configured")
		}
		jobID, err := deps.Workers.Asking(ctx, coordinatorActor(tctx), name)
		if err != nil {
			return "", workerToolReject("answer_decision", name, "", err)
		}
		captureWorkerSubject(tctx, svc.Queue, jobID)
		out, err := svc.AnswerJob(ctx, strings.TrimSpace(tctx.Identity.SessionID), name, jobID, answer, resolvedBy)
		if err != nil {
			return "", err
		}
		if tctx.Effects.Out != nil {
			subject := tctx.Effects.Out.DisplaySubject
			if subject != "" {
				subject += " · "
			}
			tctx.SetDisplaySubject(subject + out.Option)
		}
		raw, _ := json.Marshal(map[string]any{
			"status":      "resumed",
			"worker":      name,
			"answer":      out.Option,
			"resolved_by": out.ResolvedBy,
		})
		return string(raw), nil
	})
}

// AnswerDecisionToolDeps wires the coordinator answer_decision tool.
type AnswerDecisionToolDeps struct {
	Answer *AnswerDecisionService
	// Workers resolves the worker a call names to its asking round.
	Workers *workerops.Service
}

type overlayTaskLookup interface {
	Get(jobID string) (*api.WorkerTask, bool)
}
