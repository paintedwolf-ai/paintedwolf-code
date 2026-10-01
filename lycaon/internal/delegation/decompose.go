package delegation

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/pkg/api"
)

// PlanReader loads approved plans and tasks for decomposition.
type PlanReader interface {
	Get(ctx context.Context, projectID, path string) (*api.Blueprint, error)
}

// LegsFromPlan builds delegation legs with completion criteria from plan tasks.
func LegsFromPlan(p *api.Blueprint, tasks []blueprint.Task) []api.Leg {
	if len(tasks) == 0 {
		return []api.Leg{defaultLeg(p)}
	}
	legs := make([]api.Leg, 0, len(tasks))
	for _, task := range tasks {
		title := strings.TrimSpace(task.Title)
		if title == "" {
			title = "Plan task"
		}
		id := strings.TrimSpace(task.ID)
		if id == "" {
			id = uuid.NewString()
		}
		prompt := title
		if len(task.Files) > 0 {
			prompt = fmt.Sprintf("%s (files: %s)", title, strings.Join(task.Files, ", "))
		}
		legs = append(legs, api.Leg{
			ID:                 id,
			Title:              title,
			Prompt:             prompt,
			DependsOn:          append([]string(nil), task.DependsOn...),
			Files:              append([]string(nil), task.Files...),
			CompletionCriteria: blueprint.CompletionCriteria(task),
			Status:             api.LegStatusPending,
		})
	}
	return legs
}

func defaultLeg(p *api.Blueprint) api.Leg {
	prompt := "Implement approved plan"
	if p != nil && strings.TrimSpace(p.Title) != "" {
		prompt = "Implement plan: " + p.Title
	}
	return api.Leg{
		ID:                 uuid.NewString(),
		Title:              "Worker leg",
		Prompt:             prompt,
		Files:              []string{"**/*"},
		CompletionCriteria: append([]string(nil), defaultCompletionCriteria...),
		Status:             api.LegStatusPending,
	}
}
