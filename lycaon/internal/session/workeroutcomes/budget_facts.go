package workeroutcomes

import (
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

// WorkerBudgetExhaustedCode is emitted on partial worker summaries when the child
// hit iteration_cap closeout without a verified completion report.
const WorkerBudgetExhaustedCode = "WORKER_BUDGET_EXHAUSTED"

// WorkerResumeCeiling is the ceiling a resume of task should carry: the
// worker's own unanswered request when it made one, else double the spent
// ceiling, within the host maximum.
func WorkerResumeCeiling(task *api.WorkerTask, budget spawn.WorkerToolBudget) int {
	if task == nil {
		return budget.Default
	}
	if task.BudgetRequest != nil && task.BudgetRequest.RequestedMax > 0 {
		return budget.Clamp(task.BudgetRequest.RequestedMax)
	}
	return budget.Clamp(budget.Effective(task.MaxToolLoops) * 2)
}

// BudgetExhausted reports a partial finish that spent its whole ceiling.
func BudgetExhausted(task *api.WorkerTask, budget spawn.WorkerToolBudget) bool {
	if task == nil || task.Result == nil || task.Result.Status != string(api.WorkerSummaryStatusPartial) {
		return false
	}
	return task.ToolLoopsUsed >= budget.Effective(task.MaxToolLoops)
}

// BudgetFacts projects a job's durable ceiling state for coordinator guidance.
func BudgetFacts(task *api.WorkerTask, budget spawn.WorkerToolBudget) kick.WorkerBudgetFacts {
	facts := kick.WorkerBudgetFacts{
		JobID:          task.ID,
		ChildSessionID: task.ChildSessionID,
		Used:           task.ToolLoopsUsed,
		Max:            budget.Effective(task.MaxToolLoops),
		HostMax:        budget.Max,
		Request:        task.BudgetRequest,
	}
	if BudgetExhausted(task, budget) {
		facts.Exhausted = true
		facts.ResumeMax = WorkerResumeCeiling(task, budget)
	}
	return facts
}

func ChildHitIterationCapCloseout(msgs []api.Message) bool {
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleUser && msg.Kind == api.MessageKindIterationCapCloseout {
			return true
		}
	}
	return false
}

// BudgetExhaustedHint tags a partial summary whose child reached its ceiling.
func BudgetExhaustedHint(
	status string,
	task *api.WorkerTask,
	childMessages []api.Message,
	budget spawn.WorkerToolBudget,
) (string, map[string]any) {
	if strings.TrimSpace(status) != string(api.WorkerSummaryStatusPartial) || task == nil {
		return "", nil
	}
	if !ChildHitIterationCapCloseout(childMessages) {
		return "", nil
	}
	return WorkerBudgetExhaustedCode, map[string]any{
		"job_id":               strings.TrimSpace(task.ID),
		"child_session_id":     strings.TrimSpace(task.ChildSessionID),
		"tool_loops_used":      task.ToolLoopsUsed,
		"max_tool_loops":       budget.Effective(task.MaxToolLoops),
		"suggested_resume_max": WorkerResumeCeiling(task, budget),
	}
}
