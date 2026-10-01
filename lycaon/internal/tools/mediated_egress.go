package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
)

// RecordMediatedEgress appends endpoints observed over one tool-scoped proxy lease.
func RecordMediatedEgress(ctx context.Context, tctx ToolContext, toolName string, hosts []confine.EgressHost) {
	if tctx.AuthzRecorder == nil || len(hosts) == 0 {
		return
	}
	ctx = authzledger.WithInvocation(ctx, tctx.SessionID, tctx.ParentSessionID, tctx.ToolCallID)
	endpoints := make([]authzledger.CapabilityEndpoint, 0, len(hosts))
	for _, host := range hosts {
		endpoints = append(endpoints, authzledger.CapabilityEndpoint{
			Host: host.Host, Port: host.Port, Transport: host.Transport,
			Allowed: host.Allowed, Attempts: host.Attempts,
		})
	}
	sockets := make([]authzledger.CapabilitySocket, 0, len(tctx.SocketGrants))
	for _, grant := range tctx.SocketGrants {
		sockets = append(sockets, authzledger.CapabilitySocket{
			ApprovedPath: grant.ApprovedPath, ResolvedPath: grant.ResolvedPath,
			Scope: "current_action",
		})
	}
	tctx.AuthzRecorder.AppendMediatedEndpoint(ctx, authzledger.MediatedEndpointRecord{
		SessionID: tctx.SessionID, Tool: toolName,
		AuthorizationSource:  tctx.SocketAuthorizationSource,
		Endpoints:            endpoints,
		Sockets:              sockets,
		DeclaredDestinations: append([]string(nil), tctx.DirectIPDeclared...),
		Direct:               tctx.DirectIPRequested,
		FoldIntoApplied:      len(sockets) > 0 || tctx.DirectIPRequested,
	})
}
