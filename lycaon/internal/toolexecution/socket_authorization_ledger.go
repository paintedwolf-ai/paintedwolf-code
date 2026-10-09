package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/tools"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
)

func (e *Capabilities) recordCapabilityRequested(ctx context.Context, tc tools.ToolContext, tool string, grants []confine.SocketGrant) {
	if e == nil || e.Approvals.authzRecorder == nil || len(grants) == 0 {
		return
	}
	sockets := make([]authzledger.CapabilitySocket, 0, len(grants))
	for _, g := range grants {
		sockets = append(sockets, authzledger.CapabilitySocket{
			ApprovedPath: g.ApprovedPath,
			ResolvedPath: g.ResolvedPath,
			Scope:        "current_action",
		})
	}
	_ = e.Approvals.authzRecorder.AppendCapabilityRecord(ctx, authzledger.CapabilityRecord{
		SessionID: tc.SessionID,
		Action:    authzledger.ActionCapabilityRequested,
		Outcome:   authzledger.OutcomeAllowed,
		Tool:      tool,
		Sockets:   sockets,
	})
}

func (e *Capabilities) recordCapabilityDecision(ctx context.Context, tc tools.ToolContext, tool string, grant confine.SocketGrant, allowed bool, authSource string) {
	if e == nil || e.Approvals.authzRecorder == nil {
		return
	}
	action := authzledger.ActionCapabilityGranted
	outcome := authzledger.OutcomeAllowed
	if !allowed {
		action = authzledger.ActionCapabilityDenied
		outcome = authzledger.OutcomeDenied
	}
	_ = e.Approvals.authzRecorder.AppendCapabilityRecord(ctx, authzledger.CapabilityRecord{
		SessionID:           tc.SessionID,
		Action:              action,
		Outcome:             outcome,
		ResolvedBy:          authzledger.ResolvedByHuman,
		Tool:                tool,
		AuthorizationSource: authSource,
		Sockets: []authzledger.CapabilitySocket{{
			ApprovedPath: grant.ApprovedPath,
			ResolvedPath: grant.ResolvedPath,
			Scope:        "current_action",
		}},
	})
}

// EmitDirectIPLifecycle emits a typed unobserved lifecycle fact when a hook is wired.
