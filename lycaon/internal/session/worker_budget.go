package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// WorkerBudgetExhaustedCode is emitted on partial worker summaries when the child
// hit iteration_cap closeout without a verified completion report.
const WorkerBudgetExhaustedCode = "WORKER_BUDGET_EXHAUSTED"

// workerToolBudgetForTask resolves ceiling bounds from the worker's parent session.
func (m *Manager) workerToolBudgetForTask(ctx context.Context, task *api.WorkerTask) spawn.WorkerToolBudget {
	budget := spawn.DefaultWorkerToolBudget()
	if m == nil || task == nil {
		return budget
	}
	if parent, err := m.SessionByID(ctx, strings.TrimSpace(task.ParentSessionID)); err == nil && parent != nil {
		budget = m.effectiveLimits(ctx, parent).WorkerToolBudget()
	}
	return budget
}

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

// workerBudgetExhausted reports a partial finish that spent its whole ceiling.
func workerBudgetExhausted(task *api.WorkerTask, budget spawn.WorkerToolBudget) bool {
	if task == nil || task.Result == nil || task.Result.Status != string(api.WorkerSummaryStatusPartial) {
		return false
	}
	return task.ToolLoopsUsed >= budget.Effective(task.MaxToolLoops)
}

// workerBudgetFacts projects a job's durable ceiling state for coordinator guidance.
func workerBudgetFacts(task *api.WorkerTask, budget spawn.WorkerToolBudget) kick.WorkerBudgetFacts {
	facts := kick.WorkerBudgetFacts{
		JobID:          task.ID,
		ChildSessionID: task.ChildSessionID,
		Used:           task.ToolLoopsUsed,
		Max:            budget.Effective(task.MaxToolLoops),
		HostMax:        budget.Max,
		Request:        task.BudgetRequest,
	}
	if workerBudgetExhausted(task, budget) {
		facts.Exhausted = true
		facts.ResumeMax = WorkerResumeCeiling(task, budget)
	}
	return facts
}

// NotifyWorkerBudgetRequested wakes the coordinator that owns a worker's budget.
func (m *Manager) NotifyWorkerBudgetRequested(ctx context.Context, task api.WorkerTask) {
	parentID := strings.TrimSpace(task.ParentSessionID)
	if m == nil || parentID == "" || task.BudgetRequest == nil {
		return
	}
	facts := workerBudgetFacts(&task, m.workerToolBudgetForTask(ctx, &task))
	m.ensureCoordinatorRuntime().CoordinatorLoop().Nudges.NudgeWorkerBudgetRequested(ctx, parentID, task.ID, anchor.Envelope{WorkerBudget: &facts})
}

// workerBudgetRequestOpen reports a live job still carrying an unanswered
// request; a job's finish wake reports any request it ended with.
func (m *Manager) workerBudgetRequestOpen(jobID string) bool {
	jobID = strings.TrimSpace(jobID)
	if m == nil || m.workerQueue == nil || jobID == "" {
		return true
	}
	task, ok := m.workerQueue.Get(jobID)
	if !ok || task == nil || task.BudgetRequest == nil {
		return false
	}
	return task.Status == api.WorkerStatusPending || task.Status == api.WorkerStatusRunning
}

func childHitIterationCapCloseout(msgs []api.Message) bool {
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleUser && msg.Kind == api.MessageKindIterationCapCloseout {
			return true
		}
	}
	return false
}

// maybeWorkerBudgetExhaustedHint tags a partial summary whose child reached its ceiling.
func maybeWorkerBudgetExhaustedHint(
	status string,
	task *api.WorkerTask,
	childMessages []api.Message,
	budget spawn.WorkerToolBudget,
) (string, map[string]any) {
	if strings.TrimSpace(status) != string(api.WorkerSummaryStatusPartial) || task == nil {
		return "", nil
	}
	if !childHitIterationCapCloseout(childMessages) {
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
