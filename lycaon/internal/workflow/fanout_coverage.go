package workflow

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// FanoutLegCoverage accounts for attempts, independently of model conclusions.
type FanoutLegCoverage struct {
	ID        string   `json:"id"`
	AgentType string   `json:"agent_type"`
	Subject   string   `json:"subject"`
	Attempts  []string `json:"attempts"`
	Status    string   `json:"status"`
	Settled   bool     `json:"settled"`
}

// FanoutCoverage retains every planned leg, including ones never dispatched.
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

// WorkflowWork resolves planned survey legs and registered review questions.
func (m *RunManager) WorkflowWork(ctx context.Context, sessionID, workID string) (spawn.WorkflowWork, bool, error) {
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return spawn.WorkflowWork{}, false, nil
	}
	run, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || run == nil {
		return spawn.WorkflowWork{}, false, err
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return spawn.WorkflowWork{}, false, err
	}
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok {
		return spawn.WorkflowWork{}, false, nil
	}
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return spawn.WorkflowWork{}, false, err
	}
	plan, planned := FanoutPlanForPhase(vars, def)
	if !planned {
		if def.ReviewLoop == nil || def.ReviewLoop.FollowupAttempts == 0 {
			return spawn.WorkflowWork{}, false, nil
		}
		questions, err := reviewQuestions(vars, def.ID)
		if err != nil {
			return spawn.WorkflowWork{}, false, err
		}
		if slices.ContainsFunc(questions, func(q reviewQuestionWork) bool { return q.ID == workID || q.ID+"/review" == workID }) {
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
func (m *RunManager) BindWorkflowTask(ctx context.Context, tctx tools.ToolContext, workID string, task *api.WorkerTask) error {
	run, err := m.Store.ActiveBySession(ctx, tctx.Identity.SessionID)
	if err != nil {
		return err
	}
	if run == nil {
		if workID != "" {
			return rejectFanoutTask("no_active_workflow", task)
		}
		return nil
	}
	task.WorkflowRunID, task.WorkflowPhase, task.WorkflowWorkID = run.ID, run.CurrentPhase, strings.TrimSpace(workID)
	return m.AssertWorkerTask(ctx, task)
}

// AssertWorkerTask is called again under queue admission to serialize attempts.
func (m *RunManager) AssertWorkerTask(ctx context.Context, task *api.WorkerTask) error {
	run, err := m.Store.Get(ctx, task.WorkflowRunID)
	if err != nil {
		return err
	}
	if run == nil || run.CurrentPhase != task.WorkflowPhase {
		return rejectFanoutTask("workflow_phase_changed", task)
	}
	manifest, err := m.manifestForRun(ctx, run)
	if err != nil {
		return err
	}
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok {
		return fmt.Errorf("worker workflow phase is unavailable")
	}
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return err
	}
	plan, planned := FanoutPlanForPhase(vars, def)
	if !planned {
		if def.ReviewLoop != nil && def.ReviewLoop.FollowupAttempts > 0 && task.WorkflowWorkID != "" {
			return m.assertQuestionTask(ctx, run, *def.ReviewLoop, vars, task)
		}
		if task.WorkflowWorkID != "" {
			return rejectFanoutTask("not_a_planned_fanout_phase", task)
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
			return rejectFanoutTask("recovery_child_belongs_to_another_leg", task)
		}
	}
	for _, leg := range FanoutCoverage(plan, tasks, run.CurrentPhase) {
		if leg.ID != task.WorkflowWorkID {
			continue
		}
		if leg.AgentType != task.AgentType {
			return rejectFanoutTask("planned_agent_mismatch", task)
		}
		if leg.Settled {
			return rejectFanoutTask("planned_leg_settled", task)
		}
		if len(leg.Attempts) > 0 {
			switch leg.Status {
			case "pending", "running", "waiting", "held":
				return rejectFanoutTask("planned_leg_already_active", task)
			}
		}
		return nil
	}
	return rejectFanoutTask("unknown_planned_leg", task)
}

func rejectFanoutTask(reason string, task *api.WorkerTask) error {
	return &toolrejection.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{"tool": "task", "field": "workflow_work_id", "reason": reason, "workflow_work_id": task.WorkflowWorkID, "workflow_phase": task.WorkflowPhase}}
}

func (m *RunManager) stampFanoutCoverage(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, error) {
	plan, ok := fanoutPlanForExecution(vars, run.CurrentPhase)
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
	coverage := FanoutCoverage(plan, tasks, run.CurrentPhase)
	settled := len(coverage) > 0
	for _, leg := range coverage {
		settled = settled && leg.Settled
	}
	vars = SetHostVar(vars, "fanout_coverage", coverage)
	vars = SetHostVar(vars, "fanout_settled", settled)
	return vars, nil
}
