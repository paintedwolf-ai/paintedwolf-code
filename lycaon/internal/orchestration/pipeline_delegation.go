package orchestration

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// PipelineDelegation dispatches worker legs for pipeline topologies.
type PipelineDelegation interface {
	DispatchLeg(ctx context.Context, delegationID, legID, sourceToolCallID string) (*api.Leg, error)
	Abort(ctx context.Context, delegationID, reason string) error
}

type pipelineLegResumer interface {
	ResumeLeg(ctx context.Context, delegationID, legID string) (*api.Leg, error)
}

// PipelineDelegationStore persists pipeline legs and delegations.
type PipelineDelegationStore interface {
	Get(ctx context.Context, delegationID string) (*api.Delegation, error)
	Create(ctx context.Context, delegation api.Delegation, sessionID string, legs []api.Leg) (*api.Delegation, error)
	GetLeg(ctx context.Context, delegationID, legID string) (*api.Leg, error)
	UpdateLeg(ctx context.Context, leg api.Leg) error
	ListLegs(ctx context.Context, delegationID string) ([]api.Leg, error)
	DelegationBySessionID(sessionID string) (string, bool)
	DelegationByWorkflowRunID(ctx context.Context, workflowRunID string) (string, bool, error)
}
