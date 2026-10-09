package workflow

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	toolguard "github.com/lycaon/lycaon/internal/workflow/toolguard"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// runstate.FanoutLegCoverage accounts for attempts, independently of model conclusions.

// runstate.FanoutCoverage retains every planned leg, including ones never dispatched.

// WorkflowWork resolves planned survey legs and registered review questions.
func (m *Fanout) WorkflowWork(ctx context.Context, sessionID, workID string) (spawn.WorkflowWork, bool, error) {
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return spawn.WorkflowWork{}, false, nil
	}
	run, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return spawn.WorkflowWork{}, false, err
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return spawn.WorkflowWork{}, false, err
	}
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok {
		return spawn.WorkflowWork{}, false, nil
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return spawn.WorkflowWork{}, false, err
	}
	plan, planned := runstate.FanoutPlanForPhase(vars, def)
	if !planned {
		if def.ReviewLoop == nil || def.ReviewLoop.FollowupAttempts == 0 {
			return spawn.WorkflowWork{}, false, nil
		}
		known, err := m.Questions.WorkKnown(vars, def.ID, workID)
		if err != nil {
			return spawn.WorkflowWork{}, false, err
		}
		if known {
			return spawn.WorkflowWork{RunID: run.ID, Phase: run.CurrentPhase, Scope: &api.TaskScope{Mode: "read"}}, true, nil
		}
		return spawn.WorkflowWork{}, false, nil
	}
	for _, leg := range plan.Legs {
		if leg.ID == workID {
			return spawn.WorkflowWork{RunID: run.ID, Phase: run.CurrentPhase, AgentType: leg.AgentType, Scope: leg.Scope, MaxToolLoops: leg.MaxToolLoops}, true, nil
		}
	}
	return spawn.WorkflowWork{}, false, nil
}

// BindWorkflowTask stamps provenance before the native task enters the queue.
func (m *Fanout) BindWorkflowTask(ctx context.Context, tctx tools.ToolContext, workID string, task *api.WorkerTask) error {
	run, err := m.Runs.ActiveBySession(ctx, tctx.Identity.SessionID)
	if err != nil {
		return err
	}
	if run == nil {
		if workID != "" {
			return toolguard.RejectFanoutTask("no_active_workflow", task)
		}
		return nil
	}
	task.WorkflowRunID, task.WorkflowPhase, task.WorkflowWorkID = run.ID, run.CurrentPhase, strings.TrimSpace(workID)
	return m.AssertWorkerTask(ctx, task)
}

// AssertWorkerTask is called again under queue admission to serialize attempts.
func (m *Fanout) AssertWorkerTask(ctx context.Context, task *api.WorkerTask) error {
	run, err := m.Runs.Get(ctx, task.WorkflowRunID)
	if err != nil {
		return err
	}
	if run == nil || run.CurrentPhase != task.WorkflowPhase {
		return toolguard.RejectFanoutTask("workflow_phase_changed", task)
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return err
	}
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok {
		return fmt.Errorf("worker workflow phase is unavailable")
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return err
	}
	plan, planned := runstate.FanoutPlanForPhase(vars, def)
	if !planned {
		if def.ReviewLoop != nil && def.ReviewLoop.FollowupAttempts > 0 && task.WorkflowWorkID != "" {
			return m.Questions.AssertTask(ctx, run, *def.ReviewLoop, vars, task)
		}
		if task.WorkflowWorkID != "" {
			return toolguard.RejectFanoutTask("not_a_planned_fanout_phase", task)
		}
		return nil
	}
	if m.WorkerTasks == nil {
		return fmt.Errorf("workflow worker ledger unavailable")
	}
	tasks, err := m.WorkerTasks(ctx, run.ID)
	if err != nil {
		return err
	}
	for _, prior := range tasks {
		if prior.SourceToolCallID != "" && prior.SourceToolCallID == task.SourceToolCallID && prior.ParentSessionID == task.ParentSessionID {
			return nil
		}
		if task.ChildSessionID != "" && prior.ChildSessionID == task.ChildSessionID && (prior.WorkflowWorkID != task.WorkflowWorkID || prior.WorkflowPhase != task.WorkflowPhase) {
			return toolguard.RejectFanoutTask("recovery_child_belongs_to_another_leg", task)
		}
	}
	for _, leg := range runstate.FanoutCoverage(plan, tasks, run.CurrentPhase) {
		if leg.ID != task.WorkflowWorkID {
			continue
		}
		if leg.AgentType != task.AgentType {
			return toolguard.RejectFanoutTask("planned_agent_mismatch", task)
		}
		if leg.Settled {
			return toolguard.RejectFanoutTask("planned_leg_settled", task)
		}
		if len(leg.Attempts) > 0 {
			switch leg.Status {
			case "pending", "running", "waiting", "held":
				return toolguard.RejectFanoutTask("planned_leg_already_active", task)
			}
		}
		return nil
	}
	return toolguard.RejectFanoutTask("unknown_planned_leg", task)
}

func (m *Fanout) stampFanoutCoverage(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, error) {
	plan, ok := runstate.FanoutPlanForExecution(vars, run.CurrentPhase)
	if !ok {
		return vars, nil
	}
	if m.WorkerTasks == nil {
		return vars, fmt.Errorf("workflow worker ledger unavailable")
	}
	tasks, err := m.WorkerTasks(ctx, run.ID)
	if err != nil {
		return vars, err
	}
	coverage := runstate.FanoutCoverage(plan, tasks, run.CurrentPhase)
	settled := len(coverage) > 0
	for _, leg := range coverage {
		settled = settled && leg.Settled
	}
	vars = runstate.SetHostVar(vars, "fanout_coverage", coverage)
	vars = runstate.SetHostVar(vars, "fanout_settled", settled)
	return vars, nil
}
