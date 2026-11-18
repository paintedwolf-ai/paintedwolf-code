package worker

import (
	"context"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReconcileProjectSandboxes retains live branches and settles stale trees before removal.
func ReconcileProjectSandboxes(
	ctx context.Context,
	sandboxRoot, primaryDir string,
	settle workspace.SandboxSettler,
	loadTasks func(context.Context) ([]api.WorkerTask, error),
	laneRoots func(context.Context) ([]string, error),
) (removed int, err error) {
	return workspace.ReconcileStaleSandboxes(ctx, sandboxRoot, primaryDir, settle, func(ctx context.Context) (map[string]struct{}, error) {
		tasks, loadErr := loadTasks(ctx)
		if loadErr != nil {
			return nil, loadErr
		}
		retained := ActiveSandboxBranches(tasks)
		for _, task := range tasks {
			if task.EffectiveScope().IsWrite() && task.WorkspaceOverlayPath != "" && task.MergeStatus == api.WorkerMergeStatusPending {
				retainTaskBranch(retained, task)
			}
		}
		if err := RetainLanes(ctx, retained, laneRoots); err != nil {
			return nil, err
		}
		return retained, nil
	})
}

// RetainLanes adds every open worker's current lane to retained branch names.
func RetainLanes(ctx context.Context, retained map[string]struct{}, laneRoots func(context.Context) ([]string, error)) error {
	if laneRoots == nil {
		return nil
	}
	roots, err := laneRoots(ctx)
	if err != nil {
		return err
	}
	for _, root := range roots {
		if name := branchName(root); name != "" {
			retained[name] = struct{}{}
		}
	}
	return nil
}

func retainTaskBranch(retained map[string]struct{}, task api.WorkerTask) {
	retained[task.ID] = struct{}{}
	if name := branchName(task.WorkspaceRoot); name != "" {
		retained[name] = struct{}{}
	}
}

// ActiveSandboxBranches returns the branch names whose on-disk trees
// project-open reconciliation keeps for live and pending jobs.
func ActiveSandboxBranches(tasks []api.WorkerTask) map[string]struct{} {
	out := make(map[string]struct{})
	for _, task := range tasks {
		if !task.EffectiveScope().IsWrite() {
			continue
		}
		switch task.MergeStatus {
		case api.WorkerMergeStatusMerged,
			api.WorkerMergeStatusOrphaned,
			api.WorkerMergeStatusRejected,
			api.WorkerMergeStatusAborted:
			continue
		case api.WorkerMergeStatusPending, api.WorkerMergeStatusApplying, api.WorkerMergeStatusRebasing:
		}
		switch task.Status {
		case api.WorkerStatusFailed, api.WorkerStatusCanceled:
			continue
		case api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusWaiting, api.WorkerStatusComplete, api.WorkerStatusHeld:
		}
		retainTaskBranch(out, task)
	}
	return out
}
