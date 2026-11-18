package worker

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func (q *BranchService) resolveWriteCompletionTarget(ctx context.Context, claimed *api.WorkerTask, result api.WorkerResult) (*api.WorkerTask, api.WorkerMergeStatus, bool) {
	task := claimed
	if !task.EffectiveScope().IsWrite() {
		return task, "", false
	}
	if strings.TrimSpace(task.WorkspaceRoot) == "" {
		if loaded, ok := q.store.getTask(ctx, claimed.ID); ok && loaded != nil {
			loaded.ClaimToken = claimed.ClaimToken
			task = loaded
		}
	}
	layeredDiverges, layeredHandled := hasLayeredOverlayDivergence(ctx, task)
	switch {
	case strings.TrimSpace(result.Status) == "needs_decision":
		return task, "", false
	case layeredHandled && layeredDiverges:
		return task, api.WorkerMergeStatusPending, false
	case layeredHandled:
		return task, api.WorkerMergeStatusAborted, true
	case session.OverlayAwaitingPromote(ctx, task):
		return task, api.WorkerMergeStatusPending, false
	case q.overlayDivergesFromProject(ctx, task):
		workerWorkspaceLog.Warn("write overlay retained despite empty baseline diff",
			"job_id", claimed.ID, "branch_root", strings.TrimSpace(task.WorkspaceRoot))
		return task, api.WorkerMergeStatusPending, false
	default:
		return task, api.WorkerMergeStatusAborted, true
	}
}

// keptCheckpoint publishes the checkpoint of a write round that ends
// without delivering, so its changes stay under its result.
func (q *BranchService) keptCheckpoint(ctx context.Context, task *api.WorkerTask) string {
	if task == nil || !task.EffectiveScope().IsWrite() || strings.TrimSpace(task.WorkspaceRoot) == "" ||
		strings.TrimSpace(task.WorkspaceOverlayPath) != "" {
		return ""
	}
	if _, status, _ := q.resolveWriteCompletionTarget(ctx, task, api.WorkerResult{}); status != api.WorkerMergeStatusPending {
		return ""
	}
	published, err := workspace.PublishBranchCheckpoint(ctx, task.WorkspaceRoot)
	if err != nil {
		workerWorkspaceLog.Warn("ended round kept its branch without a checkpoint", "job_id", task.ID, "error", err)
		return ""
	}
	if !published {
		return ""
	}
	_, checkpointID, _ := workspace.BranchCheckpoint(task.WorkspaceRoot)
	return checkpointID
}
