package toolexecution

import (
	"github.com/lycaon/lycaon/internal/capabilitygrants"

	"context"
	"github.com/lycaon/lycaon/internal/tools"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostresources"
)

func sortedHostResourceIDs(states map[string]hostresources.State) []string {
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// permitCoveredHostResourceSockets projects matching host-resource leases.
func (e *Capabilities) permitCoveredHostResourceSockets(
	ctx context.Context,
	action hitl.ProposedAction,
	actionDigest string,
	missing []confine.SocketGrant,
	tc tools.ToolContext,
) (covered bool, extras []confine.SocketGrant) {
	if e == nil || e.Approvals.approvalGate == nil || !e.Approvals.approvalGate.HostResourceLeaseCovers(action) {
		return false, missing
	}
	for _, grant := range missing {
		if !socketMatchesRealization(grant, tc.Host.RealizationSockets) {
			extras = append(extras, grant)
			continue
		}
		if e.socketRuntime != nil {
			e.socketRuntime.IssuePermit(tc.Identity.SessionID, tc.Identity.ToolCallID, actionDigest, grant)
		}
		e.recordCapabilityDecision(ctx, tc, action.Invocation.Tool, grant, true, authzledger.AuthorizationSourceLease)
	}
	return true, extras
}

func realizationSocketTargets(resolution *hostresources.ActionResolution) []string {
	if resolution == nil {
		return nil
	}
	out := make([]string, 0, len(resolution.Connections.LocalServices))
	for _, service := range resolution.Connections.LocalServices {
		if target := strings.TrimSpace(service.Target); target != "" {
			out = append(out, target)
		}
	}
	return out
}

func socketMatchesRealization(grant confine.SocketGrant, realization []string) bool {
	if len(realization) == 0 {
		return false
	}
	approved := strings.TrimSpace(grant.ApprovedPath)
	resolved := strings.TrimSpace(grant.ResolvedPath)
	for _, target := range realization {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		if target == approved || target == resolved {
			return true
		}
	}
	return false
}

func (e *Capabilities) durableSocketGrants(tc tools.ToolContext) []confine.SocketGrant {
	if e == nil || e.durableSockets == nil {
		return nil
	}
	return e.durableSockets(tc.Identity.ProjectID)
}

func mergeAuthorizedWithOverlay(authorized, requested, overlay []confine.SocketGrant) []confine.SocketGrant {
	byPair := capabilitygrants.SocketGrantPairSet(overlay)
	out := append([]confine.SocketGrant(nil), authorized...)
	seen := capabilitygrants.SocketGrantPairSet(out)
	for _, req := range requested {
		key := capabilitygrants.SocketGrantPairKey(req)
		if _, ok := byPair[key]; !ok {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		out = append(out, req)
		seen[key] = struct{}{}
	}
	return out
}
