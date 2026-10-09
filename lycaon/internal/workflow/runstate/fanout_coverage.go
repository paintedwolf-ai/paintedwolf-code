package runstate

import (
	"sort"

	"github.com/lycaon/lycaon/pkg/api"
)

type FanoutLegCoverage struct {
	ID        string   `json:"id"`
	AgentType string   `json:"agent_type"`
	Subject   string   `json:"subject"`
	Attempts  []string `json:"attempts"`
	Status    string   `json:"status"`
	Settled   bool     `json:"settled"`
}

func FanoutCoverage(plan FanoutPlan, tasks []api.WorkerTask, phase string) []FanoutLegCoverage {
	tasks = append([]api.WorkerTask(nil), tasks...)
	sort.Slice(tasks, func(i, j int) bool {
		if !tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
			return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
		}
		return tasks[i].ID < tasks[j].ID
	})
	out := make([]FanoutLegCoverage, 0, len(plan.Legs))
	for _, leg := range plan.Legs {
		entry := FanoutLegCoverage{ID: leg.ID, AgentType: leg.AgentType, Subject: leg.Subject, Status: "not_started"}
		for _, task := range tasks {
			if task.WorkflowWorkID != leg.ID || task.WorkflowPhase != phase {
				continue
			}
			entry.Attempts = append(entry.Attempts, task.ID)
			entry.Status = api.WorkerTaskLegStatus(task)
			entry.Settled = false
			if task.Status.IsTerminal() {
				if entry.Status == "complete" {
					entry.Settled = true
				}
				if !entry.Settled && len(entry.Attempts) >= max(1, plan.MaxAttempts) {
					entry.Settled = true
				}
			}
		}
		out = append(out, entry)
	}
	return out
}
