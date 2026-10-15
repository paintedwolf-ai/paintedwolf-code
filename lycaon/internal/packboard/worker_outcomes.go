package packboard

import (
	"fmt"
	"sort"
	"strings"
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

// partialWorkerLines pairs the latest returned partial with its exact child identity.
func partialWorkerLines(tasks []api.WorkerTask) []string {
	latest := make(map[string]api.WorkerTask)
	for _, task := range tasks {
		child := strings.TrimSpace(task.ChildSessionID)
		if child == "" {
			continue
		}
		prior, found := latest[child]
		if !found || task.CreatedAt.After(prior.CreatedAt) || (task.CreatedAt.Equal(prior.CreatedAt) && task.ID > prior.ID) {
			latest[child] = task
		}
	}
	var lines []string
	for child, task := range latest {
		if !task.Status.IsTerminal() || api.WorkerTaskLegStatus(task) != "partial" || strings.TrimSpace(task.ID) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("Partial: job_id=%s · child_session_id=%s", task.ID, child))
	}
	sort.Strings(lines)
	return lines
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
	if pathLine := FormatPromotePathStatusLine(PromotePathsFromSnapshot(snapshot)); pathLine != "" {
		lines = append(lines, pathLine)
	}
	lines = append(lines, partialWorkerLines(tasks)...)
	return lines
}
