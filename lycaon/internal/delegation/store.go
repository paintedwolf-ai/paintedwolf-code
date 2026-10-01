package delegation

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// Store persists delegations and legs.
type Store interface {
	Create(ctx context.Context, delegation api.Delegation, sessionID string, legs []api.Leg) (*api.Delegation, error)
	// CreateOnce commits a delegation with an API create's receipt, or answers
	// the delegation an earlier create with the same operation made.
	CreateOnce(ctx context.Context, delegation api.Delegation, sessionID string, legs []api.Leg, receipt CreateReceipt) (*api.Delegation, error)
	// DelegationByOperation answers the delegation an operation created.
	DelegationByOperation(ctx context.Context, receipt CreateReceipt) (*api.Delegation, bool, error)
	AddLeg(ctx context.Context, delegationID string, leg api.Leg) error
	Get(ctx context.Context, delegationID string) (*api.Delegation, error)
	// Settle closes an active delegation whose legs are terminal; only the transition winner returns true.
	Settle(ctx context.Context, delegationID string) (bool, error)
	GetLeg(ctx context.Context, delegationID, legID string) (*api.Leg, error)
	UpdateLeg(ctx context.Context, leg api.Leg) error
	// RecordLegOutcome changes outcome fields only while the same worker owns an unsettled leg.
	RecordLegOutcome(ctx context.Context, leg api.Leg) (bool, error)
	Abort(ctx context.Context, delegationID string, completedAt time.Time, reason string) error
	DispatchLegWithJob(ctx context.Context, leg api.Leg, delegation api.Delegation, task api.WorkerTask) error
	RedispatchLegWithJob(ctx context.Context, leg api.Leg, delegation api.Delegation, task api.WorkerTask) error
	ListLegs(ctx context.Context, delegationID string) ([]api.Leg, error)
	ListByProject(ctx context.Context, projectDir, sessionID string) ([]api.Delegation, error)
	DelegationBySessionID(sessionID string) (string, bool)
	DelegationByWorkflowRunID(ctx context.Context, workflowRunID string) (string, bool, error)
	SessionID(delegationID string) (string, bool)
}
