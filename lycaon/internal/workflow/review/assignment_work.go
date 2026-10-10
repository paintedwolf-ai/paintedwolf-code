package review

import (
	"context"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"slices"
)

func (r *Assignments) Work(run *api.WorkflowRun, def workflowdef.ReviewLoopDef, workID string) (spawn.WorkflowWork, bool) {
	for _, agent := range runstate.DedupeReviewAgents(def.RequiredAgents, def.IfSpawnable) {
		if workID == reviewWorkID(agent) {
			return spawn.WorkflowWork{RunID: run.ID, Phase: run.CurrentPhase, AgentType: agent, Scope: &api.TaskScope{Mode: "read"}}, true
		}
	}
	return spawn.WorkflowWork{}, false
}

func (r *Assignments) AssertDeclared(ctx context.Context, run *api.WorkflowRun, def workflowdef.ReviewLoopDef, task *api.WorkerTask) (bool, error) {
	if task.WorkflowWorkID != reviewWorkID(task.AgentType) && task.WorkflowWorkID != "" {
		return false, nil
	}
	declared := slices.Contains(runstate.DedupeReviewAgents(def.RequiredAgents, def.IfSpawnable), task.AgentType)
	if def.AssignmentBinding == "explicit" && declared && task.WorkflowWorkID == "" {
		return true, toolguard.RejectFanoutTask("review_work_id_required", task)
	}
	if task.WorkflowWorkID != "" && !declared {
		return true, toolguard.RejectFanoutTask("undeclared_reviewer", task)
	}
	if declared && (def.AssignmentBinding == "explicit" || len(def.CoverageReviewers) > 0 || task.WorkflowWorkID != "") {
		return true, r.AssertInitial(ctx, run, task)
	}
	return true, nil
}
