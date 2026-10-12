package llm

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

func (c *RoutingClient) acquireLocalCall(ctx context.Context, sel *ModelSelection) (func(), error) {
	if c.plane == nil || c.registry == nil || sel == nil {
		return nil, nil
	}
	provider, err := c.registry.Get(sel.ProviderID)
	if err != nil {
		return nil, err
	}
	if !provider.Profile().UtilitySingleFlight {
		return nil, nil
	}
	return c.plane.acquireForeground(ctx, provider)
}

// Admission lasts until the upstream stream has drained.
func releaseAfterStream(ctx context.Context, ch <-chan modelcall.StreamChunk, release func()) <-chan modelcall.StreamChunk {
	if release == nil {
		return ch
	}
	if ch == nil {
		release()
		return nil
	}
	out := make(chan modelcall.StreamChunk)
	go func() {
		defer close(out)
		defer release()
		for chunk := range ch {
			if !modelcall.SendChunk(ctx, out, chunk) {
				modelcall.DrainStream(ch)
				return
			}
		}
	}()
	return out
}
