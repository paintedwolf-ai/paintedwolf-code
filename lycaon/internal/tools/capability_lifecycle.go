package tools

import (
	"github.com/lycaon/lycaon/internal/capabilitygrants"

	"context"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
)

func recordSocketCapabilityApplied(ctx context.Context, tctx ToolContext, grants []confine.SocketGrant) {
	if tctx.Local.AuthzRecorder == nil || len(grants) == 0 {
		return
	}
	durableByPair := capabilitygrants.SocketGrantPairSet(tctx.Socket.DurableSocketGrants)
	sockets := make([]authzledger.CapabilitySocket, 0, len(grants))
	for _, g := range grants {
		scope := "current_action"
		if _, ok := durableByPair[capabilitygrants.SocketGrantPairKey(g)]; ok {
			scope = "project"
		} else if tctx.Socket.SocketCapabilityRuntime != nil {
			for _, chat := range tctx.Socket.SocketCapabilityRuntime.AppliedGrants(tctx.ChatSessionID()) {
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
	_ = tctx.Local.AuthzRecorder.AppendCapabilityRecord(ctx, authzledger.CapabilityRecord{
		SessionID:            tctx.Identity.SessionID,
		Action:               authzledger.ActionCapabilityApplied,
		Outcome:              authzledger.OutcomeAllowed,
		ResolvedBy:           authzledger.ResolvedByHuman,
		Tool:                 "command",
		AuthorizationSource:  tctx.Socket.SocketAuthorizationSource,
		Sockets:              sockets,
		Direct:               tctx.Direct.DirectIPRequested,
		DeclaredDestinations: append([]string(nil), tctx.Direct.DirectIPDeclared...),
	})
}

func EmitDirectIPLifecycle(tctx ToolContext, phase DirectIPLifecyclePhase) {
	if tctx.Direct.DirectIPLifecycle == nil || !tctx.Direct.DirectIPRequested {
		return
	}
	tctx.Direct.DirectIPLifecycle(DirectIPLifecycleEvent{
		Phase:                phase,
		SessionID:            tctx.Identity.SessionID,
		ToolCallID:           tctx.Identity.ToolCallID,
		ActionDigest:         tctx.Direct.DirectIPActionDigest,
		Visibility:           hitl.DirectIPVisibilityUnobserved,
		DeclaredDestinations: append([]string(nil), tctx.Direct.DirectIPDeclared...),
		Background:           tctx.Direct.DirectIPBackground,
	})
}
