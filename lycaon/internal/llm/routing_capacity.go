package llm

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

func (c *RoutingClient) openWithCapacity(ctx context.Context, sel *ModelSelection, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, *ModelSelection, error) {
	if sel != nil {
		// Standing refusals avoid repeated calls.
		if refused := c.refusals.Err(sel.ProviderID, sel.Model); refused != nil {
			return nil, sel, refused
		}
		if err := c.capacity.Await(ctx, sel.ProviderID, sel.Model); err != nil {
			return nil, sel, err
		}
	}
	ch, err := c.openOnce(ctx, sel, req)
	for hold := 0; c.capacity.ShouldHold(err, hold); hold++ {
		if holdErr := c.capacity.Hold(ctx, hold); holdErr != nil {
			if sel != nil {
				c.capacity.NoteFailure(sel.ProviderID, sel.Model, err)
			}
			return nil, sel, holdErr
		}
		ch, err = c.openOnce(ctx, sel, req)
	}
	if err != nil {
		if sel != nil {
			c.capacity.NoteFailure(sel.ProviderID, sel.Model, err)
			c.refusals.Note(sel.ProviderID, sel.Model, err)
		}
		return nil, sel, stampSelection(err, sel)
	}
	if sel != nil {
		c.capacity.NoteSuccess(sel.ProviderID, sel.Model)
		c.refusals.Clear(sel.ProviderID, sel.Model)
		ch = c.observeStreamOutcome(ctx, sel, ch)
	}
	return c.occupyLocalStream(ctx, sel, ch), sel, nil
}

// observeStreamOutcome records what a stream established once it ends. A
// transport that answers inside the stream, as Converse does, reports a
// refusal or an unavailable slot as the terminal chunk, and those facts count
// the same as ones returned when the stream opened. A stream the consumer
// abandons establishes nothing.
func (c *RoutingClient) observeStreamOutcome(ctx context.Context, sel *ModelSelection, ch <-chan modelcall.StreamChunk) <-chan modelcall.StreamChunk {
	out := make(chan modelcall.StreamChunk, 64)
	go func() {
		defer close(out)
		var terminal error
		for chunk := range ch {
			if chunk.Err != nil {
				terminal = chunk.Err
			}
			if !modelcall.SendChunk(ctx, out, chunk) {
				modelcall.DrainStream(ch)
				return
			}
		}
		if terminal == nil {
			return
		}
		c.capacity.NoteFailure(sel.ProviderID, sel.Model, terminal)
		c.refusals.Note(sel.ProviderID, sel.Model, terminal)
	}()
	return out
}

func (c *RoutingClient) openOnce(ctx context.Context, sel *ModelSelection, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	if c.registry != nil && sel != nil {
		primary, err := c.registry.StreamWithSelection(ctx, sel, req)
		if err != nil {
			return nil, stampSelection(fmt.Errorf("provider %s: %w", sel.ProviderID, err), sel)
		}
		return primary, nil
	}
	if sel != nil {
		return nil, &failure.ProviderNotConfiguredError{ProviderID: sel.ProviderID}
	}
	return nil, fmt.Errorf("no AI provider available")
}

// stampSelection fills the routed provider and model onto provider failures
// that reached the caller without them, so notice copy can name who failed.
func stampSelection(err error, sel *ModelSelection) error {
	if err == nil || sel == nil {
		return err
	}
	if e, ok := failure.AsProviderOverloaded(err); ok && e != nil && e.Model == "" {
		e.ProviderID, e.Model = stampedIdentity(e.ProviderID, sel)
	}
	if e, ok := failure.AsProviderRateLimited(err); ok && e != nil && e.Model == "" {
		e.ProviderID, e.Model = stampedIdentity(e.ProviderID, sel)
	}
	if e, ok := failure.AsProviderSilent(err); ok && e != nil && e.Model == "" {
		e.ProviderID, e.Model = stampedIdentity(e.ProviderID, sel)
	}
	if e, ok := failure.AsProviderUnreachable(err); ok && e != nil && e.Model == "" {
		e.ProviderID, e.Model = stampedIdentity(e.ProviderID, sel)
	}
	return err
}

func stampedIdentity(providerID string, sel *ModelSelection) (string, string) {
	if providerID == "" {
		providerID = sel.ProviderID
	}
	return providerID, sel.Model
}
