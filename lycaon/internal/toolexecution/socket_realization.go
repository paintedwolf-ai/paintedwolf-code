package toolexecution

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/capabilitygrants"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostresources"
	"github.com/lycaon/lycaon/internal/tools"
)

func sortedHostResourceIDs(states map[string]hostresources.State) []string {
	ids := make([]string, 0, len(states))
	for id := range states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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
