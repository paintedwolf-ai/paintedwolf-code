package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/internal/workspacebaseline"
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

type branchCompletion struct {
	task         *api.WorkerTask
	mergeStatus  api.WorkerMergeStatus
	teardown     bool
	overlayPath  string
	checkpointID string
}

func (q *BranchService) prepareCompletion(ctx context.Context, claimed *api.WorkerTask, result api.WorkerResult) (branchCompletion, error) {
	task, mergeStatus, teardown := q.resolveWriteCompletionTarget(ctx, claimed, result)
	overlayPath, checkpointID := "", ""
	// Checkpoint-backed views publish directly; baseline-backed branches capture their delta.
	// A budget-exceeded overlay stays unsealed for review.
	if mergeStatus == api.WorkerMergeStatusPending && strings.TrimSpace(task.WorkspaceOverlayPath) == "" {
		published, err := workspace.PublishBranchCheckpoint(ctx, task.WorkspaceRoot)
		if err != nil {
			return branchCompletion{}, fmt.Errorf("worker %s: publish checkpoint: %w", task.ID, err)
		}
		if published {
			_, checkpointID, _ = workspace.BranchCheckpoint(task.WorkspaceRoot)
		}
		if !published && strings.TrimSpace(task.WorkspaceBaselinePath) != "" {
			captured, err := q.captureOverlay(ctx, task)
			if err != nil {
				if errors.Is(err, workspacebaseline.ErrOverlayBudgetExceeded) {
					workerWorkspaceLog.Warn("write overlay exceeds budget; leaving unsealed for review",
						"job_id", task.ID, "error", err)
				} else {
					return branchCompletion{}, err
				}
			} else {
				overlayPath = captured
			}
		}
	}
	return branchCompletion{task: task, mergeStatus: mergeStatus, teardown: teardown, overlayPath: overlayPath, checkpointID: checkpointID}, nil
}
