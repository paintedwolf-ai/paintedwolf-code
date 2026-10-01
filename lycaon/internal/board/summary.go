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

	var inFlight, complete, failed int
	var pendingMerge, merged, rebasing, orphaned, rejected int
	var conflicts int

	for _, t := range tasks {
		switch t.Status {
		case api.WorkerStatusComplete:
			complete++
		case api.WorkerStatusFailed, api.WorkerStatusCanceled:
			failed++
		default:
			inFlight++
		}
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
	case failed > 0 && complete == 0:
		parts = append(parts, fmt.Sprintf("%d worker(s) failed", failed))
	case complete == len(tasks):
		parts = append(parts, "All workers complete")
	default:
		parts = append(parts, "Workers finished")
	}
	if complete > 0 && inFlight == 0 {
		parts = append(parts, fmt.Sprintf("%d complete", complete))
	}
	if failed > 0 && (inFlight > 0 || complete > 0) {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
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
