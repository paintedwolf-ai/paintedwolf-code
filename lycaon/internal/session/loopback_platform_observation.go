package session

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/lineage"
)

// ObserveSessionListener supplies current complete native holder evidence to
// mediated connection admission. The returned identity grants no authority;
// partial visibility retains ordinary approval rather than cached provenance.
func (p *DefaultLoopbackProvenance) ObserveSessionListener(ctx context.Context, sessionID string, endpoint netip.AddrPort) (string, bool, error) {
	if p == nil {
		return "", false, lineage.ErrObservationIncomplete
	}
	if p.listeners == nil {
		return lineage.ObserveSessionListener(ctx, sessionID, endpoint)
	}
	if sessionID == "" || !endpoint.IsValid() || endpoint.Port() == 0 || !endpoint.Addr().Unmap().IsLoopback() {
		return "", false, nil
	}
	current, err := p.listeners(ctx)
	if err != nil {
		return "", false, err
	}
	port := endpoint.Port()
	targetAddr := endpoint.Addr().Unmap()
	canonicalEndpoint := netip.AddrPortFrom(targetAddr, port)
	held := onPort(current, port)
	var endpointPIDs []int
	for _, l := range held {
		if l.PID <= 0 || !l.Addr.IsValid() {
			return "", false, fmt.Errorf("port %d has a listener with incomplete holder observation", port)
		}
		if l.Addr.Unmap() == targetAddr {
			endpointPIDs = append(endpointPIDs, l.PID)
		}
	}
	owned, evidence := p.ownership(ctx, sessionID, "", port, held)
	if owned {
		if len(held) == 0 {
			return fmt.Sprintf("%s:%s:%s:%s", evidence.Method, sessionID, evidence.OwnerID, canonicalEndpoint), true, nil
		}
		if len(endpointPIDs) == 0 {
			return "", false, nil
		}
		return fmt.Sprintf("%s:%s:%s:%s", evidence.Method, sessionID, canonicalEndpoint, joinSortedPIDs(endpointPIDs)), true, nil
	}
	if len(endpointPIDs) > 0 {
		return fmt.Sprintf("foreign:%s:%s", canonicalEndpoint, joinSortedPIDs(endpointPIDs)), false, nil
	}
	return "", false, nil
}

func joinSortedPIDs(pids []int) string {
	sorted := slices.Clone(pids)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	parts := make([]string, len(sorted))
	for i, pid := range sorted {
		parts[i] = strconv.Itoa(pid)
	}
	return strings.Join(parts, ",")
}
