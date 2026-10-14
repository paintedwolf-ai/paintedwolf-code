package packboard

import (
	"fmt"

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
