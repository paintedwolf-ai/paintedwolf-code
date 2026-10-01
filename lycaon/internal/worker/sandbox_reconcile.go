package worker

import (
	"context"

	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReconcileProjectSandboxes removes snapshotted sandboxes absent from current task state.
func ReconcileProjectSandboxes(
	ctx context.Context,
	sandboxRoot, primaryDir string,
	loadTasks func(context.Context) ([]api.WorkerTask, error),
) (removed int, err error) {
	return workspace.ReconcileStaleSandboxes(ctx, sandboxRoot, primaryDir, func(ctx context.Context) (map[string]struct{}, error) {
		tasks, loadErr := loadTasks(ctx)
		if loadErr != nil {
			return nil, loadErr
		}
		return ActiveSandboxJobIDs(tasks), nil
	})
}

// ActiveSandboxJobIDs returns the worker job IDs whose on-disk sandboxes
// project-open reconciliation keeps.
func ActiveSandboxJobIDs(tasks []api.WorkerTask) map[string]struct{} {
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
		out[task.ID] = struct{}{}
	}
	return out
}
