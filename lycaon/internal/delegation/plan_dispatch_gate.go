package delegation

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/pkg/api"
)

// PlanApprovalChecker verifies plan approval for dispatch.
type PlanApprovalChecker interface {
	IsApproved(ctx context.Context, projectID, blueprintPath string) (bool, error)
}

// WorkflowReadyChecker evaluates implement_workflow_ready for dispatch.
type WorkflowReadyChecker interface {
	ImplementWorkflowReady(ctx context.Context, sessionID, blueprintPath string) (bool, error)
}

// PlanApprovalDispatchGate blocks dispatch until the linked plan is approved.
type PlanApprovalDispatchGate struct {
	Inner DispatchGate
	Store Store
	Plans PlanApprovalChecker
}

// Check allows dispatch when plan is approved or no plan is linked.
func (g PlanApprovalDispatchGate) Check(ctx context.Context, delegationID, legID string) (bool, string, error) {
	if g.Plans != nil && g.Store != nil {
		delegation, err := g.Store.Get(ctx, delegationID)
		if err != nil {
			return false, "", err
		}
		if delegation != nil && strings.TrimSpace(delegation.BlueprintPath) != "" {
			ok, err := g.Plans.IsApproved(ctx, delegation.ProjectID, delegation.BlueprintPath)
			if err != nil {
				return false, "", err
			}
			if !ok {
				return false, "plan not approved", nil
			}
		}
	}
	if g.Inner == nil {
		return true, "", nil
	}
	return g.Inner.Check(ctx, delegationID, legID)
}

// ImplementWorkflowDispatchGate blocks dispatch until plan approval and implement handoff are ready.
type ImplementWorkflowDispatchGate struct {
	Inner         DispatchGate
	Store         Store
	Plans         PlanApprovalChecker
	WorkflowReady WorkflowReadyChecker
}

// Check allows dispatch when plan is approved and implement workflow is ready.
func (g ImplementWorkflowDispatchGate) Check(ctx context.Context, delegationID, legID string) (bool, string, error) {
	if g.Plans != nil && g.Store != nil {
		delegation, err := g.Store.Get(ctx, delegationID)
		if err != nil {
			return false, "", err
		}
		if delegation != nil && strings.TrimSpace(delegation.BlueprintPath) != "" {
			ok, err := g.Plans.IsApproved(ctx, delegation.ProjectID, delegation.BlueprintPath)
			if err != nil {
				return false, "", err
			}
			if !ok {
				return false, "plan not approved", nil
			}
			if g.WorkflowReady != nil {
				ready, err := g.WorkflowReady.ImplementWorkflowReady(ctx, delegation.CoordinatorSessionID, delegation.BlueprintPath)
				if err != nil {
					return false, "", err
				}
				if !ready {
					return false, "implement workflow not ready", nil
				}
			}
		}
	}
	if g.Inner == nil {
		return true, "", nil
	}
	return g.Inner.Check(ctx, delegationID, legID)
}

// PopulateLegCriteria stamps completion criteria from plan tasks when empty.
func PopulateLegCriteria(ctx context.Context, plans PlanReader, delegation *api.Delegation, leg *api.Leg) error {
	if leg == nil || len(leg.CompletionCriteria) > 0 {
		return nil
	}
	if delegation == nil || strings.TrimSpace(delegation.BlueprintPath) == "" || plans == nil {
		leg.CompletionCriteria = append([]string(nil), defaultCompletionCriteria...)
		return nil
	}
	p, err := plans.Get(ctx, delegation.ProjectID, delegation.BlueprintPath)
	if err != nil {
		return err
	}
	tasks := blueprint.ExtractTasks(p.Content)
	for _, task := range tasks {
		if task.ID == leg.ID || (len(tasks) == 1) {
			leg.CompletionCriteria = blueprint.CompletionCriteria(task)
			return nil
		}
	}
	leg.CompletionCriteria = append([]string(nil), defaultCompletionCriteria...)
	return nil
}
