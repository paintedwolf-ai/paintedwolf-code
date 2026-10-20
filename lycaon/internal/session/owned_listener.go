package session

import (
	"context"
	"net/netip"
)

// ExactSessionListener observes current resource and holder identities. Port-only
// provenance and declared capabilities cannot implement this authority contract.
type ExactSessionListener interface {
	ObserveSessionListener(context.Context, string, netip.AddrPort) (string, bool, error)
}

// ObserveOwnedSessionListener is shared by preflight and mediated payload checks.
// Missing platform support or incomplete inventory retains ordinary approval.
func ObserveOwnedSessionListener(ctx context.Context, provenance LoopbackProvenanceResolver, sessionID string, endpoint netip.AddrPort) (string, bool) {
	observer, ok := provenance.(ExactSessionListener)
	if !ok || sessionID == "" || !endpoint.IsValid() || !endpoint.Addr().Unmap().IsLoopback() || endpoint.Port() == 0 || ctx.Err() != nil {
		return "", false
	}
	identity, owned, err := observer.ObserveSessionListener(ctx, sessionID, netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port()))
	return identity, err == nil && owned && identity != "" && ctx.Err() == nil
}
