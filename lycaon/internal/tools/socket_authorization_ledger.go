package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

func recordSocketCapabilityApplied(ctx context.Context, tctx ToolContext, grants []confine.SocketGrant) {
	if tctx.AuthzRecorder == nil || len(grants) == 0 {
		return
	}
	durableByPair := socketGrantPairSet(tctx.DurableSocketGrants)
	sockets := make([]authzledger.CapabilitySocket, 0, len(grants))
	for _, g := range grants {
		scope := "current_action"
		if _, ok := durableByPair[socketGrantPairKey(g)]; ok {
			scope = "project"
		} else if tctx.SocketCapabilityRuntime != nil {
			for _, chat := range tctx.SocketCapabilityRuntime.AppliedGrants(tctx.ChatSessionID()) {
				if socketGrantPairKey(chat) == socketGrantPairKey(g) {
					scope = "chat"
					break
				}
			}
		}
		sockets = append(sockets, authzledger.CapabilitySocket{
			ApprovedPath: g.ApprovedPath,
			ResolvedPath: g.ResolvedPath,
			Scope:        scope,
		})
	}
	_ = tctx.AuthzRecorder.AppendCapabilityRecord(ctx, authzledger.CapabilityRecord{
		SessionID:            tctx.SessionID,
		Action:               authzledger.ActionCapabilityApplied,
		Outcome:              authzledger.OutcomeAllowed,
		ResolvedBy:           authzledger.ResolvedByHuman,
		Tool:                 "command",
		AuthorizationSource:  tctx.SocketAuthorizationSource,
		Sockets:              sockets,
		Direct:               tctx.DirectIPRequested,
		DeclaredDestinations: append([]string(nil), tctx.DirectIPDeclared...),
	})
}

func (e *DefaultToolExecutor) recordCapabilityRequested(ctx context.Context, tc ToolContext, tool string, grants []confine.SocketGrant) {
	if e == nil || e.authzRecorder == nil || len(grants) == 0 {
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
	_ = e.authzRecorder.AppendCapabilityRecord(ctx, authzledger.CapabilityRecord{
		SessionID: tc.SessionID,
		Action:    authzledger.ActionCapabilityRequested,
		Outcome:   authzledger.OutcomeAllowed,
		Tool:      tool,
		Sockets:   sockets,
	})
}

func (e *DefaultToolExecutor) recordCapabilityDecision(ctx context.Context, tc ToolContext, tool string, grant confine.SocketGrant, allowed bool, authSource string) {
	if e == nil || e.authzRecorder == nil {
		return
	}
	action := authzledger.ActionCapabilityGranted
	outcome := authzledger.OutcomeAllowed
	if !allowed {
		action = authzledger.ActionCapabilityDenied
		outcome = authzledger.OutcomeDenied
	}
	_ = e.authzRecorder.AppendCapabilityRecord(ctx, authzledger.CapabilityRecord{
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
func EmitDirectIPLifecycle(tctx ToolContext, phase DirectIPLifecyclePhase) {
	if tctx.DirectIPLifecycle == nil || !tctx.DirectIPRequested {
		return
	}
	tctx.DirectIPLifecycle(DirectIPLifecycleEvent{
		Phase:                phase,
		SessionID:            tctx.SessionID,
		ToolCallID:           tctx.ToolCallID,
		ActionDigest:         tctx.DirectIPActionDigest,
		Visibility:           hitl.DirectIPVisibilityUnobserved,
		DeclaredDestinations: append([]string(nil), tctx.DirectIPDeclared...),
		Background:           tctx.DirectIPBackground,
	})
}
