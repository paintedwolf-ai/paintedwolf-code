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

type nativeListenerObserver func(context.Context, string, netip.AddrPort) (string, bool, error)

// ObserveSessionListener checks all port holders before using exact native or
// recorded-container evidence for the requested endpoint.
func (p *DefaultLoopbackProvenance) ObserveSessionListener(ctx context.Context, sessionID string, endpoint netip.AddrPort) (string, bool, error) {
	if p == nil {
		return "", false, lineage.ErrObservationIncomplete
	}
	if sessionID == "" || !endpoint.IsValid() || endpoint.Port() == 0 || !endpoint.Addr().Unmap().IsLoopback() {
		return "", false, nil
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if p.listeners == nil {
		return "", false, lineage.ErrObservationIncomplete
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
		if evidence.Method == ProvenanceLineage {
			if p.observeNative == nil {
				return "", false, lineage.ErrObservationIncomplete
			}
			return p.observeNative(ctx, sessionID, canonicalEndpoint)
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
