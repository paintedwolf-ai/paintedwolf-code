package worker

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	workerResumeMismatchCode     = "WORKER_RESUME_MISMATCH"
	workerResumeChildUnknownCode = "WORKER_RESUME_CHILD_UNKNOWN"
)

// taskIdentity is a task call with the fields a resumed child or planned leg supplies.
type taskIdentity struct {
	AgentType      string
	Scope          api.TaskScope
	WorkflowWorkID string
	MaxToolLoops   int
	// Prior is the resumed child's latest job.
	Prior *api.WorkerTask
}

// Resumed children retain their recorded identity and pending budget request.
func resolveTaskIdentity(ctx context.Context, deps TaskToolDeps, tctx tools.ToolContext, args map[string]any, requestedMax int, budget spawn.WorkerToolBudget) (taskIdentity, error) {
	id := taskIdentity{MaxToolLoops: requestedMax}
	id.AgentType, _ = args["agent_type"].(string)
	id.AgentType = strings.TrimSpace(id.AgentType)
	id.WorkflowWorkID, _ = args["workflow_work_id"].(string)
	id.WorkflowWorkID = strings.TrimSpace(id.WorkflowWorkID)
	_, scopeGiven := args["scope"]
	scope, err := api.TaskScopeFromArgs(args)
	if err != nil {
		return id, err
	}
	id.Scope = scope.Normalized()

	workIDGiven := id.WorkflowWorkID != ""
	childSessionID, _ := args["child_session_id"].(string)
	if childSessionID = strings.TrimSpace(childSessionID); childSessionID != "" {
		prior, ok := deps.Queue.GetLatestByChildSessionID(ctx, childSessionID)
		if !ok || prior == nil {
			return id, &tools.ToolReject{Code: workerResumeChildUnknownCode, Data: map[string]any{"child_session_id": childSessionID}}
		}
		id.Prior = prior
		if err := id.inheritFrom(prior, scopeGiven, childSessionID, budget); err != nil {
			return id, err
		}
	}

	leg, planned, err := id.workflowWork(ctx, deps, tctx, id.Prior != nil && !workIDGiven)
	if err != nil {
		return id, err
	}
	if planned {
		if id.AgentType == "" {
			id.AgentType = leg.AgentType
		}
		if !scopeGiven && id.Prior == nil && leg.Scope != nil {
			id.Scope = leg.Scope.Normalized()
		}
		if id.MaxToolLoops <= 0 {
			id.MaxToolLoops = leg.MaxToolLoops
		}
	}
	if id.AgentType == "" {
		return id, &tools.ToolReject{
			Code: "TOOL_ARGS_INVALID",
			Data: map[string]any{"reason": "missing_agent_type", "field": "agent_type", "tool": "task"},
		}
	}
	id.MaxToolLoops = budget.Effective(id.MaxToolLoops)
	return id, nil
}

func (id *taskIdentity) inheritFrom(prior *api.WorkerTask, scopeGiven bool, childSessionID string, budget spawn.WorkerToolBudget) error {
	recordedScope := prior.EffectiveScope()
	switch {
	case id.AgentType != "" && id.AgentType != prior.AgentType:
		return resumeMismatch(childSessionID, "agent_type", prior.AgentType, id.AgentType)
	case scopeGiven && id.Scope.Mode != recordedScope.Mode:
		return resumeMismatch(childSessionID, "scope", string(recordedScope.Mode), string(id.Scope.Mode))
	}
	id.AgentType = prior.AgentType
	if !scopeGiven {
		id.Scope = recordedScope
	}
	if id.WorkflowWorkID == "" {
		id.WorkflowWorkID = strings.TrimSpace(prior.WorkflowWorkID)
	}
	if id.MaxToolLoops <= 0 {
		id.MaxToolLoops = budget.Effective(prior.MaxToolLoops)
		if req := prior.BudgetRequest; req != nil && req.RequestedMax > id.MaxToolLoops {
			id.MaxToolLoops = budget.Clamp(req.RequestedMax)
		}
	}
	return nil
}

// Resumed work retains ownership only within its active run and phase.
func (id *taskIdentity) workflowWork(ctx context.Context, deps TaskToolDeps, tctx tools.ToolContext, inherited bool) (spawn.WorkflowWork, bool, error) {
	if id.WorkflowWorkID == "" || deps.WorkflowWork == nil {
		return spawn.WorkflowWork{}, false, nil
	}
	leg, ok, err := deps.WorkflowWork(ctx, tctx.SessionID, id.WorkflowWorkID)
	if err != nil {
		return spawn.WorkflowWork{}, false, err
	}
	if inherited && (!ok || leg.RunID != id.Prior.WorkflowRunID || leg.Phase != id.Prior.WorkflowPhase) {
		id.WorkflowWorkID = ""
		return spawn.WorkflowWork{}, false, nil
	}
	return leg, ok, nil
}

func resumeMismatch(childSessionID, field, recorded, requested string) error {
	return &tools.ToolReject{Code: workerResumeMismatchCode, Data: map[string]any{
		"child_session_id": childSessionID,
		"resume_field":     field,
		"resume_recorded":  recorded,
		"resume_requested": requested,
	}}
}
