package packboard

import (
	"fmt"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerOutcomeCounts separates returned delivery from execution lifecycle.
type WorkerOutcomeCounts map[string]int

func CountWorkerOutcomes(tasks []api.WorkerTask) WorkerOutcomeCounts {
	counts := WorkerOutcomeCounts{}
	for _, task := range tasks {
		if !task.Status.IsTerminal() {
			continue
		}
		status := api.WorkerTaskLegStatus(task)
		switch status {
		case "complete", "partial", "blocked", "failed", "canceled":
		default:
			status = "unreported"
		}
		counts[status]++
	}
	return counts
}

func (counts WorkerOutcomeCounts) Total() int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func (counts WorkerOutcomeCounts) Labels() []string {
	var labels []string
	for _, status := range []string{"complete", "partial", "blocked", "failed", "canceled", "unreported"} {
		if count := counts[status]; count > 0 {
			labels = append(labels, fmt.Sprintf("%d %s", count, status))
		}
	}
	return labels
}

// workerStateLines keeps completed execution distinct from pending integration.
func workerStateLines(snapshot api.BoardSnapshot, now time.Time) []string {
	tasks := WorkerTasksFromSnapshot(snapshot)
	var lines []string
	if work := FormatWorkLine(tasks, now); work != "" {
		lines = append(lines, work)
	}
	if merge := FormatBranchMergeLine(tasks); merge != "" {
		lines = append(lines, merge)
	}
	if planLine := FormatOverlayMergePlanLine(OverlayMergePlanFromSnapshot(snapshot)); planLine != "" {
		lines = append(lines, planLine)
	}
	return lines
}
