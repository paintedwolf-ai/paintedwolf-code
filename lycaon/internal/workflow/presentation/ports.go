package presentation

import (
	"context"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

type AskProjection interface {
	ReconcileCoordinatorAskProjection(context.Context, string, map[string]any)
}

type TopologyLegSource interface {
	RunTopologyLegs(context.Context, *api.WorkflowRun, string, map[string]string) ([]api.WorkflowTopologyLeg, error)
}
type ObligationStatus interface {
	Status(context.Context, *api.WorkflowRun, workflowdef.PhaseDef) []api.WorkflowRunObligation
}
type ChoiceTransitions interface {
	ChoiceTransitionArmed(context.Context, *api.WorkflowRun, workflowdef.Manifest, workflowdef.PhaseTransitionDef, map[string]any) (bool, error)
}

type StartProposal interface {
	ProposedStart(context.Context, string) (string, string, string, bool, error)
}
