package coordinatorcontrol

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/session/batchcontrol"
	"github.com/lycaon/lycaon/internal/session/turnadmission"
	"github.com/lycaon/lycaon/internal/session/turnsettlement"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

type Workers struct {
	Runtime    *coordinator.Runtime
	Batch      *batchcontrol.Service
	Settlement *turnsettlement.Service
	Admission  *turnadmission.Service
	Digests    *workeroutcomes.Digests
	Results    *workeroutcomes.Results
}

func (m *Workers) Terminal(ctx context.Context, parentID, completingJobID string) {
	if m == nil {
		return
	}
	m.Runtime.CoordinatorLoop().Cycles.OnWorkerCycleTerminal(ctx, parentID, completingJobID)
	m.Batch.Reconcile(ctx, parentID)
	m.Batch.DisarmTerminal(ctx, parentID)
	m.Settlement.ReconcileSandbox(ctx, parentID)
	m.Admission.MaybePromote(ctx, parentID)
	m.Digests.Forget(completingJobID)
}
func (m *Workers) BudgetRequested(ctx context.Context, task api.WorkerTask) {
	parentID := strings.TrimSpace(task.ParentSessionID)
	if m == nil || parentID == "" || task.BudgetRequest == nil {
		return
	}
	facts := workeroutcomes.BudgetFacts(&task, m.Results.BudgetForTask(ctx, &task))
	m.Runtime.CoordinatorLoop().Nudges.NudgeWorkerBudgetRequested(ctx, parentID, task.ID, anchor.Envelope{WorkerBudget: &facts})
}

func (m *Workers) AfterTerminal(
	ctx context.Context,
	parentID, completingJobID string,
	env anchor.Envelope,
) {
	if m == nil || strings.TrimSpace(parentID) == "" || strings.TrimSpace(completingJobID) == "" {
		return
	}
	m.Runtime.CoordinatorLoop().Nudges.NudgeAfterWorkerJobTerminal(
		ctx,
		parentID,
		completingJobID,
		anchor.WorkerTaskFinished,
		anchor.WorkerTaskFinished,
		"",
		env,
	)
}
