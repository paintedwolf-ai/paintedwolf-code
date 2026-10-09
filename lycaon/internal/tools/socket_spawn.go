package tools

import (
	"github.com/lycaon/lycaon/internal/capabilitygrants"

	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
)

// FinalizeSocketGrantsForSpawn consumes validated current-call permits.
func FinalizeSocketGrantsForSpawn(tctx ToolContext) ([]confine.SocketGrant, *toolrejection.ToolReject) {
	rt := tctx.Socket.SocketCapabilityRuntime
	grants := tctx.Socket.SocketGrants
	if len(grants) == 0 {
		return nil, nil
	}
	overlayByPair := capabilitygrants.SocketGrantPairSet(tctx.Socket.DurableSocketGrants)
	if rt != nil {
		for k := range capabilitygrants.SocketGrantPairSet(rt.AppliedGrants(tctx.ChatSessionID())) {
			overlayByPair[k] = struct{}{}
		}
	}
	out := make([]confine.SocketGrant, 0, len(grants))
	for _, g := range grants {
		g = capabilitygrants.NormalizeToolSocketGrant(g)
		if err := confine.RevalidateSocketGrant(g); err != nil {
			return nil, capabilityrequest.SocketResolveReject(err)
		}
		if _, overlay := overlayByPair[capabilitygrants.SocketGrantPairKey(g)]; !overlay {
			if rt == nil {
				return nil, &toolrejection.ToolReject{Code: isolation.CodeSocketPathChanged, Data: map[string]any{
					"path":   g.ApprovedPath,
					"reason": "missing current-call socket permit",
				}}
			}
			ok, err := rt.ConsumePermit(tctx.Identity.SessionID, tctx.Identity.ToolCallID, tctx.Socket.SocketActionDigest, g)
			if err != nil {
				return nil, &toolrejection.ToolReject{Code: isolation.CodeSocketPathChanged, Data: map[string]any{
					"path":   g.ApprovedPath,
					"reason": err.Error(),
				}}
			}
			if !ok {
				return nil, &toolrejection.ToolReject{Code: isolation.CodeSocketPathChanged, Data: map[string]any{
					"path":   g.ApprovedPath,
					"reason": "missing current-call socket permit",
				}}
			}
		}
		out = append(out, g)
	}
	return out, nil
}

// ClaimDeclaredSocket finalizes the reviewed authority for the socket a tool's
// declared socket argument names and records it as applied. The caller dials
// the returned grant's ResolvedPath, never the argument text.
func ClaimDeclaredSocket(ctx context.Context, tctx ToolContext, raw string) (confine.SocketGrant, *toolrejection.ToolReject) {
	requested := SocketArgPath(tctx, raw)
	grants, reject := FinalizeSocketGrantsForSpawn(tctx)
	if reject != nil {
		return confine.SocketGrant{}, reject
	}
	resolved := fspath.CanonicalPath(requested)
	for _, g := range grants {
		if g.ApprovedPath == requested || (resolved != "" && g.ResolvedPath == resolved) {
			recordSocketCapabilityApplied(ctx, tctx, []confine.SocketGrant{g})
			return g, nil
		}
	}
	return confine.SocketGrant{}, &toolrejection.ToolReject{Code: isolation.CodeSocketPathChanged, Data: map[string]any{
		"path":   requested,
		"reason": "no reviewed socket authority for this call",
	}}
}

// ConfineRequestForSpawn finalizes the gate-reviewed confinement.
func ConfineRequestForSpawn(ctx context.Context, tctx ToolContext, overlayWriteRoots []string) (confine.Request, *toolrejection.ToolReject) {
	req := hitl.ActionConfineRequest(ActionConfineInputsForContext(tctx, overlayWriteRoots))
	if reject := ValidateAttachedRootsForAction(req.Roots); reject != nil {
		return confine.Request{}, reject
	}
	if reject := ValidateGrantedRootsForAction(req.GrantedWriteRoots); reject != nil {
		return confine.Request{}, reject
	}
	grants, reject := FinalizeSocketGrantsForSpawn(tctx)
	if reject != nil {
		return confine.Request{}, reject
	}
	req.SocketGrants = grants
	if tctx.Direct.DirectIPRequested {
		if reject := FinalizeDirectIPForSpawn(tctx); reject != nil {
			return confine.Request{}, reject
		}
	}
	if reject := finalizeExecutionCapability(tctx, req); reject != nil {
		return confine.Request{}, reject
	}
	recordSocketCapabilityApplied(ctx, tctx, grants)
	return req, nil
}
