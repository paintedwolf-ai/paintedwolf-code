package confine

import (
	"context"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/workscope"
)

type egressResolverRegistration struct {
	fn   EgressResolver
	work workscope.Group
}

// SetEgressResolver installs the approval hook and returns its owner's drain.
func SetEgressResolver(fn EgressResolver) func(context.Context) error {
	registration := &egressResolverRegistration{fn: fn}
	egressBroker.mu.Lock()
	egressBroker.resolverOwner = registration
	egressBroker.resolver = registration.resolve
	egressBroker.mu.Unlock()
	return func(ctx context.Context) error {
		egressBroker.mu.Lock()
		if egressBroker.resolverOwner == registration {
			egressBroker.resolverOwner = nil
			egressBroker.resolver = nil
		}
		egressBroker.mu.Unlock()
		registration.work.Stop()
		if err := registration.work.Wait(ctx); err != nil {
			return err
		}
		egressBroker.mu.Lock()
		registration.fn = nil
		egressBroker.mu.Unlock()
		return nil
	}
}

func (r *egressResolverRegistration) resolve(ctx context.Context, cmd EgressCommand, ep egressproxy.Endpoint, detection *EgressDetectionCitation) bool {
	egressBroker.mu.Lock()
	fn := r.fn
	if fn == nil {
		egressBroker.mu.Unlock()
		return false
	}
	ctx, finish, err := r.work.Begin(ctx)
	egressBroker.mu.Unlock()
	if err != nil {
		return false
	}
	defer finish()
	allow := fn(ctx, cmd, ep, detection)
	return allow && ctx.Err() == nil
}
