// Package delegation defines multi-worker delegation orchestration and executor
// state. DispatchLeg applies DispatchGate before enqueueing workers.
package delegation

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

// DelegationManager manages delegations, legs, and dispatch.
// sourceToolCallID identifies the originating dispatch request (coordinator
// tool call id, or empty for host-driven dispatch) so a retried dispatch
// enqueues idempotently instead of double-dispatching the leg.
type DelegationManager interface {
	Create(ctx context.Context, req api.CreateDelegationRequest) (*api.Delegation, error)
	DispatchLeg(ctx context.Context, delegationID, legID, sourceToolCallID string) (*api.Leg, error)
	RecordOutcome(ctx context.Context, delegationID, legID, workerID string, outcome api.WorkerResult) error
	GetStatus(ctx context.Context, delegationID string) (*api.Delegation, error)
	Abort(ctx context.Context, delegationID, reason string) error
}
