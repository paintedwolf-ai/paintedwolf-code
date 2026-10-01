package workflow

import (
	"context"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/pkg/api"
)

// RegistryWorkflowReadyChecker uses conditions.ConditionRegistry for implement_workflow_ready.
type RegistryWorkflowReadyChecker struct {
	Registry *conditions.ConditionRegistry
	Sessions SessionLookup
}

// ImplementWorkflowReady evaluates the implement_workflow_ready domain predicate.
func (c RegistryWorkflowReadyChecker) ImplementWorkflowReady(ctx context.Context, sessionID, blueprintPath string) (bool, error) {
	if c.Registry == nil {
		return false, nil
	}
	var sess *api.Session
	if c.Sessions != nil && sessionID != "" {
		sess, _ = c.Sessions.Get(ctx, sessionID)
	}
	run := &api.WorkflowRun{SessionID: sessionID, BlueprintPath: blueprintPath}
	ec := conditions.EvalContextFromRun(ctx, sess, run, nil)
	ok, err := c.Registry.Evaluate("implement_workflow_ready", ec)
	return ok, err
}

var _ delegation.WorkflowReadyChecker = RegistryWorkflowReadyChecker{}
