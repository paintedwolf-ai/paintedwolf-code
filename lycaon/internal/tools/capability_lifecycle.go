package tools

import (
	"github.com/lycaon/lycaon/internal/capabilitygrants"

	"context"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

func recordSocketCapabilityApplied(ctx context.Context, tctx ToolContext, grants []confine.SocketGrant) {
	if tctx.AuthzRecorder == nil || len(grants) == 0 {
		return
	}
	durableByPair := capabilitygrants.SocketGrantPairSet(tctx.DurableSocketGrants)
	sockets := make([]authzledger.CapabilitySocket, 0, len(grants))
	for _, g := range grants {
		scope := "current_action"
		if _, ok := durableByPair[capabilitygrants.SocketGrantPairKey(g)]; ok {
			scope = "project"
		} else if tctx.SocketCapabilityRuntime != nil {
			for _, chat := range tctx.SocketCapabilityRuntime.AppliedGrants(tctx.ChatSessionID()) {
				if capabilitygrants.SocketGrantPairKey(chat) == capabilitygrants.SocketGrantPairKey(g) {
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
