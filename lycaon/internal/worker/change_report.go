package worker

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
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
