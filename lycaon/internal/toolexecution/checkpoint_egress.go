package toolexecution

import (
	"context"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

// SetCheckpointManager routes tool and egress approvals through the same checkpoint flow.
func (e *Approvals) SetCheckpointManager(ctx context.Context, mgr hitl.CheckpointManager, gate hitl.ApprovalGate) {
	if e == nil {
		return
	}
	e.checkpointMgr = mgr
	e.approvalGate = gate
	previous := e.releaseEgressResolver
	e.releaseEgressResolver = confine.SetEgressResolver(e.Network.resolveEgress)
	if previous != nil {
		_ = previous(context.WithoutCancel(ctx))
	}
}

// ReleaseEgressResolver seals broker approval admission and drains its callbacks.
func (e *Approvals) ReleaseEgressResolver(ctx context.Context) error {
	if e == nil || e.releaseEgressResolver == nil {
		return nil
	}
	if err := e.releaseEgressResolver(ctx); err != nil {
		return err
	}
	e.releaseEgressResolver = nil
	return nil
}
