package packboard

import (
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// MaxOrientationWorkers bounds named worker rows rendered inline on the board.
const MaxOrientationWorkers = 12

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

// FormatNamedWorkerLine renders one orientation row for a named worker.
func FormatNamedWorkerLine(w api.BoardWorker, includeDetail bool) string {
	name := strings.TrimSpace(w.Name)
	if name == "" {
		return ""
	}
	agent := strings.TrimSpace(w.AgentType)
	if agent == "" {
		agent = "worker"
	}
	state := strings.TrimSpace(string(w.State))
	if r := strings.TrimSpace(string(w.Reason)); r != "" {
		state += ":" + r
	}
	parts := []string{"Worker: " + name, agent, state}
	if ws := strings.TrimSpace(w.WorkspaceState); ws != "" {
		parts = append(parts, ws)
	}
	if h := strings.TrimSpace(w.ResultHandle); h != "" {
		if d := strings.TrimSpace(string(w.ResultDisposition)); d != "" {
			parts = append(parts, h+":"+d)
		} else {
			parts = append(parts, h)
		}
	}
	if q := strings.TrimSpace(w.Question); q != "" {
		parts = append(parts, "ask: "+q)
	}
	if includeDetail {
		if len(w.ChangedPaths) > 0 {
			seg := "changed: " + strings.Join(w.ChangedPaths, ",")
			if w.ChangedOmitted > 0 {
				seg += fmt.Sprintf(" (+%d)", w.ChangedOmitted)
			}
			parts = append(parts, seg)
		}
		if about := strings.TrimSpace(w.About); about != "" {
			parts = append(parts, about)
		}
	}
	return strings.Join(parts, " · ")
}

// workerStateLines keeps completed execution distinct from pending integration.
func workerStateLines(snapshot api.BoardSnapshot, now time.Time) []string {
	return workerStateLinesWithDetail(snapshot, now, false)
}

func workerStateLinesWithDetail(snapshot api.BoardSnapshot, now time.Time, includeDetail bool) []string {
	tasks := WorkerTasksFromSnapshot(snapshot)
	named := NamedWorkersFromSnapshot(snapshot)
	var lines []string
	if len(named) > 0 {
		shown := named
		if len(shown) > MaxOrientationWorkers {
			shown = shown[:MaxOrientationWorkers]
		}
		for _, w := range shown {
			if line := FormatNamedWorkerLine(w, includeDetail); line != "" {
				lines = append(lines, line)
			}
		}
	} else if work := FormatWorkLine(tasks, now); work != "" {
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
