package worker

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// filterLiveOverlays keeps the write overlays promote or reject can still
// target: in-flight write workers, and completed ones that kept a workspace and
// have no terminal merge status.
func filterLiveOverlays(tasks []api.WorkerTask) []api.WorkerTask {
	out := make([]api.WorkerTask, 0, len(tasks))
	for _, task := range tasks {
		if !task.EffectiveScope().IsWrite() {
			continue
		}
		if strings.TrimSpace(task.WorkspaceRoot) == "" && task.Status == api.WorkerStatusComplete {
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
		out = append(out, task)
	}
	return out
}
