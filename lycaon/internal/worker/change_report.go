package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspace"
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
}

// RegisterWorkerCancelTool registers worker_cancel(job_id, reason?) for coordinator sessions.
func RegisterWorkerCancelTool(reg *tools.DefaultRegistry, deps CancelToolDeps) error {
	if reg == nil || deps.Cancel == nil {
		return fmt.Errorf("registry and cancel service required")
	}
	return reg.Register("worker_cancel", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		jobID, _ := args["job_id"].(string)
		jobID = strings.TrimSpace(jobID)
		if jobID == "" {
			return "", fmt.Errorf("job_id is required")
		}
		captureWorkerSubject(tctx, deps.Cancel.Queue, jobID)
		reason, _ := args["reason"].(string)
		out, err := deps.Cancel.CancelForSession(ctx, tctx.Identity.SessionID, jobID, reason)
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(out)
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
		jobID, _ := args["job_id"].(string)
		option, _ := args["option"].(string)
		resolvedBy, _ := args["resolved_by"].(string)
		captureWorkerSubject(tctx, svc.Queue, jobID)
		out, err := svc.AnswerJob(ctx, strings.TrimSpace(tctx.Identity.SessionID), strings.TrimSpace(jobID), strings.TrimSpace(option), strings.TrimSpace(resolvedBy))
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
			"job_id":      out.JobID,
			"option":      out.Option,
			"resolved_by": out.ResolvedBy,
		})
		return string(raw), nil
	})
}

// AnswerDecisionToolDeps wires the coordinator answer_decision tool.
type AnswerDecisionToolDeps struct {
	Answer *AnswerDecisionService
}

const (
	workerBudgetDeclineNoRequestCode       = "WORKER_BUDGET_DECLINE_NO_REQUEST"
	workerBudgetDeclineSessionMismatchCode = "WORKER_BUDGET_DECLINE_SESSION_MISMATCH"
)

// DeclineBudgetToolDeps wires decline_worker_budget for coordinator sessions.
type DeclineBudgetToolDeps struct {
	Queue  WorkerQueue
	Ledger BudgetLedger
}

// RegisterDeclineWorkerBudgetTool registers decline_worker_budget(job_id).
func RegisterDeclineWorkerBudgetTool(reg *tools.DefaultRegistry, deps DeclineBudgetToolDeps) error {
	if reg == nil || deps.Queue == nil || deps.Ledger == nil {
		return fmt.Errorf("registry, queue, and budget ledger required")
	}
	return reg.Register("decline_worker_budget", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		jobID, _ := args["job_id"].(string)
		jobID = strings.TrimSpace(jobID)
		if jobID == "" {
			return "", fmt.Errorf("job_id is required")
		}
		task, ok := deps.Queue.Get(jobID)
		if !ok || task == nil {
			return "", &toolrejection.ToolReject{Code: workerBudgetDeclineNoRequestCode, Data: map[string]any{"job_id": jobID}}
		}
		if strings.TrimSpace(task.ParentSessionID) != strings.TrimSpace(tctx.Identity.SessionID) {
			return "", &toolrejection.ToolReject{Code: workerBudgetDeclineSessionMismatchCode, Data: map[string]any{"job_id": jobID}}
		}
		tctx.SetDisplaySubject(task.Brief)
		declined, err := deps.Ledger.Decline(ctx, jobID)
		if err != nil {
			return "", err
		}
		if !declined {
			return "", &toolrejection.ToolReject{Code: workerBudgetDeclineNoRequestCode, Data: map[string]any{"job_id": jobID, "status": string(task.Status)}}
		}
		out := map[string]any{
			"job_id":          jobID,
			"max_tool_loops":  task.MaxToolLoops,
			"tool_loops_used": task.ToolLoopsUsed,
		}
		if task.BudgetRequest != nil {
			out["requested_max"] = task.BudgetRequest.RequestedMax
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return "", fmt.Errorf("encode decline_worker_budget result: %w", err)
		}
		return string(raw), nil
	})
}

type overlayTaskLookup interface {
	Get(jobID string) (*api.WorkerTask, bool)
}

func workspaceSourceRoots(task *api.WorkerTask, canonical []projectroot.RootRef, overlays overlayTaskLookup) ([]projectroot.RootRef, error) {
	if task == nil {
		return nil, fmt.Errorf("worker task required")
	}
	baseID := strings.TrimSpace(task.EffectiveScope().BaseOverlayID)
	if baseID == "" {
		return append([]projectroot.RootRef(nil), canonical...), nil
	}
	if overlays == nil {
		return nil, fmt.Errorf("base overlay %q is unavailable", baseID)
	}
	base, ok := overlays.Get(baseID)
	if !ok || base == nil {
		return nil, fmt.Errorf("base overlay %q is unavailable", baseID)
	}
	if base.ID == task.ID || base.ProjectID != task.ProjectID || base.ParentSessionID != task.ParentSessionID {
		return nil, fmt.Errorf("base overlay %q does not belong to this worker lineage", baseID)
	}
	switch base.MergeStatus {
	case api.WorkerMergeStatusMerged:
		return append([]projectroot.RootRef(nil), canonical...), nil
	case api.WorkerMergeStatusRejected, api.WorkerMergeStatusOrphaned, api.WorkerMergeStatusAborted:
		return nil, fmt.Errorf("base overlay %q is %s", baseID, base.MergeStatus)
	case api.WorkerMergeStatusPending, api.WorkerMergeStatusApplying, api.WorkerMergeStatusRebasing:
	}
	if !base.EffectiveScope().IsWrite() || strings.TrimSpace(base.WorkspaceRoot) == "" {
		return nil, fmt.Errorf("base overlay %q has no complete branch", baseID)
	}
	sources, err := workspace.BranchRootRefs(base.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("base overlay %q: %w", baseID, err)
	}
	if len(sources) != len(canonical) {
		return nil, fmt.Errorf("base overlay %q root set changed", baseID)
	}
	byID := make(map[string]projectroot.RootRef, len(sources))
	for _, source := range sources {
		byID[source.ID] = source
	}
	ordered := make([]projectroot.RootRef, 0, len(canonical))
	for _, root := range canonical {
		source, found := byID[root.ID]
		if !found {
			return nil, fmt.Errorf("base overlay %q root identity changed", baseID)
		}
		source.Label = root.Label
		source.IsPrimary = root.IsPrimary
		ordered = append(ordered, source)
	}
	return ordered, nil
}
