package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

// NotifyWorkerBudgetRequested wakes the coordinator that owns a worker's budget.
func (m *Manager) NotifyWorkerBudgetRequested(ctx context.Context, task api.WorkerTask) {
	parentID := strings.TrimSpace(task.ParentSessionID)
	if m == nil || parentID == "" || task.BudgetRequest == nil {
		return
	}
	facts := workeroutcomes.BudgetFacts(&task, m.Workers.Results.BudgetForTask(ctx, &task))
	m.ensureCoordinatorRuntime().CoordinatorLoop().NudgeWorkerBudgetRequested(ctx, parentID, task.ID, anchor.Envelope{WorkerBudget: &facts})
}
