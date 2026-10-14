package board

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

// BuildBoardSummary renders a one-line coordination headline for pack_board output.
func BuildBoardSummary(snap api.BoardSnapshot) string {
	tasks := packboard.WorkerTasksFromSnapshot(snap)
	promotePaths := packboard.PromotePathsFromSnapshot(snap)

	outcomes := packboard.CountWorkerOutcomes(tasks)
	inFlight := len(tasks) - outcomes.Total()
	var pendingMerge, merged, rebasing, orphaned, rejected int
	var conflicts int

	for _, t := range tasks {
		switch t.MergeStatus {
		case api.WorkerMergeStatusPending, api.WorkerMergeStatusApplying:
			pendingMerge++
		case api.WorkerMergeStatusMerged:
			merged++
		case api.WorkerMergeStatusRebasing:
			rebasing++
		case api.WorkerMergeStatusOrphaned:
			orphaned++
		case api.WorkerMergeStatusRejected:
			rejected++
		case api.WorkerMergeStatusAborted:
		}
	}
	for _, job := range promotePaths {
		for _, p := range job.Paths {
			if p.Status == api.WorkerPromotePathOutcomeConflict {
				conflicts++
			}
		}
	}

	var parts []string
	switch {
	case inFlight > 0:
		parts = append(parts, fmt.Sprintf("%d worker(s) in flight", inFlight))
	case len(tasks) == 0:
		parts = append(parts, "No workers")
	case outcomes["complete"] == len(tasks):
		parts = append(parts, "All workers complete")
	default:
		parts = append(parts, "Workers returned")
	}
	parts = append(parts, outcomes.Labels()...)
	if merged > 0 {
		parts = append(parts, fmt.Sprintf("%d overlay(s) merged", merged))
	}
	if pendingMerge > 0 {
		parts = append(parts, fmt.Sprintf("%d overlay(s) pending merge", pendingMerge))
	}
	if rebasing > 0 {
		parts = append(parts, fmt.Sprintf("%d overlay(s) rebasing", rebasing))
	}
	if orphaned > 0 {
		parts = append(parts, fmt.Sprintf("%d overlay(s) orphaned", orphaned))
	}
	if rejected > 0 {
		parts = append(parts, fmt.Sprintf("%d overlay(s) rejected", rejected))
	}
	if conflicts > 0 {
		parts = append(parts, fmt.Sprintf("%d path(s) in conflict (preview)", conflicts))
	} else if pendingMerge > 0 || rebasing > 0 {
		if len(promotePaths) > 0 {
			parts = append(parts, "no preview conflicts cached")
		} else {
			parts = append(parts, "promote or reject pending overlays")
		}
	}
	return strings.Join(parts, ". ") + "."
}
