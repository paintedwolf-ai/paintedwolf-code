package confine

import (
	"context"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/workscope"
	"net/netip"
)

// OwnedLoopbackResolver observes an exact listener under current session and
// network policy. Its opaque resource identity is not a grant or cached lease.
type OwnedLoopbackResolver func(context.Context, EgressCommand, netip.AddrPort) (string, bool)

type ownedLoopbackRegistration struct {
	observe OwnedLoopbackResolver
	work    workscope.Group
}

// SetOwnedLoopbackResolver installs the observation boundary and returns its
// conditional shutdown. Releasing an older registration cannot remove a newer one.
func SetOwnedLoopbackResolver(resolver OwnedLoopbackResolver) func(context.Context) error {
	registration := &ownedLoopbackRegistration{observe: resolver}
	egressBroker.mu.Lock()
	egressBroker.ownedLoopback = registration
	egressBroker.mu.Unlock()
	return func(ctx context.Context) error {
		egressBroker.mu.Lock()
		if egressBroker.ownedLoopback == registration {
			egressBroker.ownedLoopback = nil
		}
		egressBroker.mu.Unlock()
		registration.work.Stop()
		return registration.work.Wait(ctx)
	}
}

func (r *ownedLoopbackRegistration) resolve(ctx context.Context, command EgressCommand, endpoint netip.AddrPort) (string, bool) {
	if r == nil || r.observe == nil {
		return "", false
	}
	admitted, finish, err := r.work.Begin(ctx)
	if err != nil {
		return "", false
	}
	defer finish()
	id, ok := r.observe(admitted, command, endpoint)
	return id, ok && id != "" && admitted.Err() == nil
}

func (b *egressBrokerT) ownedLoopbackFor(ctx context.Context, lineage string, endpoint netip.AddrPort) (string, bool) {
	if !endpoint.IsValid() || !endpoint.Addr().Unmap().IsLoopback() {
		return "", false
	}
	b.mu.Lock()
	command, live := b.tokens[lineage]
	observe := b.ownedLoopback
	b.mu.Unlock()
	if !live || observe == nil || command.SessionID == "" || ctx.Err() != nil {
		return "", false
	}
	return observe.resolve(ctx, command, netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port()))
}

func (b *egressBrokerT) reportLoopbackOwnershipRefusal(_ context.Context, lineage string, endpoint egressproxy.Endpoint) {
	// Retain the earlier allowed/connected observation: refusing later payload
	// does not undo bytes already delivered through the same connection.
	b.observeEndpoint(lineage, endpoint, false)
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.egress[lineage] {
		observed := &b.egress[lineage][i]
		if sameEgressEndpoint(*observed, endpoint) && !observed.Allowed {
			observed.Outcome = "authority_refused"
			return
		}
	}
}
