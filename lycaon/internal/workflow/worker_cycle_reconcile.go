package workflow

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReconcileWorkerCycle repairs planned completion from the retained job ledger.
func (m *Fanout) ReconcileWorkerCycle(ctx context.Context, sessionID string) error {
	run, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil || run.Status != api.WorkflowRunStatusRunning {
		return err
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return err
	}
	plan, ok := runstate.FanoutPlanForExecution(vars, run.CurrentPhase)
	if !ok {
		return nil
	}
	if m.WorkerTasks == nil {
		return fmt.Errorf("workflow worker ledger unavailable")
	}
	tasks, err := m.WorkerTasks(ctx, run.ID)
	if err != nil {
		return err
	}
	coverage := runstate.FanoutCoverage(plan, tasks, run.CurrentPhase)
	for _, leg := range coverage {
		if !leg.Settled {
			return nil
		}
	}
	// Recovery excludes no pending delivery from the idle gate.
	return m.recordWorkerTerminalProof(ctx, run, "", "")
}
