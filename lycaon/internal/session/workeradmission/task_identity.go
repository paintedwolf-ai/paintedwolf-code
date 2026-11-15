package workeradmission

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

// TaskIdentity is a task call with the fields a planned leg supplies.
type TaskIdentity struct {
	MutationCapable bool
	AgentType       string
	Scope           api.TaskScope
	WorkflowWorkID  string
	Charter         *api.WorkerTaskCharter
}

// TaskIdentityResolver resolves the identity a workflow leg supplies.
type TaskIdentityResolver struct {
	WorkflowWork func(context.Context, string, string) (spawn.WorkflowWork, bool, error)
}

func (r TaskIdentityResolver) Resolve(ctx context.Context, sessionID string, args map[string]any) (TaskIdentity, error) {
	var id TaskIdentity
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

	leg, planned, err := id.workflowWork(ctx, r, sessionID)
	if err != nil {
		return id, err
	}
	if planned {
		id.Charter = leg.Charter
		if id.AgentType == "" {
			id.AgentType = leg.AgentType
		}
		if !scopeGiven && leg.Scope != nil {
			id.Scope = leg.Scope.Normalized()
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
	return id, nil
}

func (id *TaskIdentity) workflowWork(ctx context.Context, r TaskIdentityResolver, sessionID string) (spawn.WorkflowWork, bool, error) {
	if id.WorkflowWorkID == "" || r.WorkflowWork == nil {
		return spawn.WorkflowWork{}, false, nil
	}
	return r.WorkflowWork(ctx, sessionID, id.WorkflowWorkID)
}
