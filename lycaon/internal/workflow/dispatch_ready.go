package workflow

import (
	"context"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/pkg/api"
)

// DispatchReadyRunLookup reads the active workflow run and scaffold state for a session.
type DispatchReadyRunLookup interface {
	ActiveBySession(ctx context.Context, sessionID string) (*api.WorkflowRun, error)
	GetScaffoldVars(ctx context.Context, runID string) (map[string]any, error)
}

// RegistryWorkflowReadyChecker uses conditions.ConditionRegistry for implement_workflow_ready.
type RegistryWorkflowReadyChecker struct {
	Registry *conditions.ConditionRegistry
	Sessions SessionLookup
	Runs     DispatchReadyRunLookup
}

// ImplementWorkflowReady evaluates implement handoff readiness for blueprintPath.
func (c RegistryWorkflowReadyChecker) ImplementWorkflowReady(ctx context.Context, sessionID, blueprintPath string) (bool, error) {
	if c.Registry == nil {
		return false, nil
	}
	var sess *api.Session
	if c.Sessions != nil && sessionID != "" {
		sess, _ = c.Sessions.Get(ctx, sessionID)
	}
	var run *api.WorkflowRun
	var vars map[string]any
	if c.Runs != nil && sessionID != "" {
		if active, err := c.Runs.ActiveBySession(ctx, sessionID); err == nil && active != nil {
			run = active
			vars, _ = c.Runs.GetScaffoldVars(ctx, active.ID)
		}
	}
	if sess != nil && sess.Posture != api.SessionPostureSpec && (run == nil || run.BlueprintPath != blueprintPath) {
		return true, nil
	}
	if run == nil {
		run = &api.WorkflowRun{SessionID: sessionID, BlueprintPath: blueprintPath}
	}
	ec := conditions.EvalContextFromRun(ctx, sess, run, vars)
	if ec.BlueprintPath == "" {
		ec.BlueprintPath = blueprintPath
	}
	return c.Registry.Evaluate("implement_workflow_ready", ec)
}

var _ delegation.WorkflowReadyChecker = RegistryWorkflowReadyChecker{}
