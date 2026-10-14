package workeradmission

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	workerResumeMismatchCode     = "WORKER_RESUME_MISMATCH"
	workerResumeChildUnknownCode = "WORKER_RESUME_CHILD_UNKNOWN"
)

// TaskIdentity is a task call with the fields a resumed child or planned leg supplies.
type TaskIdentity struct {
	MutationCapable bool
	AgentType       string
	Scope           api.TaskScope
	WorkflowWorkID  string
	MaxToolLoops    int
	// Prior is the resumed child's latest job.
	Prior   *api.WorkerTask
	Charter *api.WorkerTaskCharter
}

// TaskIdentityResolver resolves the identity supplied by an existing child or workflow leg.
type TaskIdentityResolver struct {
	Children interface {
		GetLatestByChildSessionID(context.Context, string) (*api.WorkerTask, bool)
	}
	WorkflowWork func(context.Context, string, string) (spawn.WorkflowWork, bool, error)
	ToolBudget   func(string) spawn.WorkerToolBudget
}

func (r TaskIdentityResolver) Resolve(ctx context.Context, sessionID, projectDir string, args map[string]any) (TaskIdentity, error) {
	requestedMax, err := ParseTaskMaxToolLoopsFromArgs(args)
	if err != nil {
		return TaskIdentity{}, err
	}
	budget := spawn.DefaultWorkerToolBudget()
	if r.ToolBudget != nil {
		budget = r.ToolBudget(projectDir)
	}
	if code := ValidateTaskMaxToolLoopsCode(requestedMax, budget); code != "" {
		return TaskIdentity{}, &toolrejection.ToolReject{Code: code, Data: map[string]any{"max_tool_loops": requestedMax, "min_required": budget.Min, "host_max": budget.Max}}
	}

	id := TaskIdentity{MaxToolLoops: requestedMax}
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
		var prior *api.WorkerTask
		var ok bool
		if r.Children != nil {
			prior, ok = r.Children.GetLatestByChildSessionID(ctx, childSessionID)
		}
		if !ok || prior == nil {
			return id, &toolrejection.ToolReject{Code: workerResumeChildUnknownCode, Data: map[string]any{"child_session_id": childSessionID}}
		}
		id.Prior = prior
		if err := id.inheritFrom(prior, scopeGiven, childSessionID, budget); err != nil {
			return id, err
		}
	}

	leg, planned, err := id.workflowWork(ctx, r, sessionID, id.Prior != nil && !workIDGiven)
	if err != nil {
		return id, err
	}
	if planned {
		if id.Prior == nil {
			id.Charter = leg.Charter
		}
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
		return id, &toolrejection.ToolReject{
			Code: "TOOL_ARGS_INVALID",
			Data: map[string]any{"reason": "missing_agent_type", "field": "agent_type", "tool": "task"},
		}
	}
	capable, known := prompts.AgentMutationCapable(id.AgentType)
	if !known {
		return id, &toolrejection.ToolReject{Code: "WORKER_TYPE_UNAVAILABLE", Data: map[string]any{"agent_type": id.AgentType, "reason": "profile_unavailable"}}
	}
	id.MutationCapable = capable
	id.MaxToolLoops = budget.Effective(id.MaxToolLoops)
	return id, nil
}

func (id *TaskIdentity) inheritFrom(prior *api.WorkerTask, scopeGiven bool, childSessionID string, budget spawn.WorkerToolBudget) error {
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

// Resumed work retains its assignment only within its active run and phase.
func (id *TaskIdentity) workflowWork(ctx context.Context, r TaskIdentityResolver, sessionID string, inherited bool) (spawn.WorkflowWork, bool, error) {
	if id.WorkflowWorkID == "" || r.WorkflowWork == nil {
		return spawn.WorkflowWork{}, false, nil
	}
	leg, ok, err := r.WorkflowWork(ctx, sessionID, id.WorkflowWorkID)
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
	return &toolrejection.ToolReject{Code: workerResumeMismatchCode, Data: map[string]any{
		"child_session_id": childSessionID,
		"resume_field":     field,
		"resume_recorded":  recorded,
		"resume_requested": requested,
	}}
}
