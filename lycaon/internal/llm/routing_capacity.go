package llm

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

func (c *RoutingClient) openWithCapacity(ctx context.Context, sel *ModelSelection, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, *ModelSelection, error) {
	callID := firstNonEmpty(req.Debug.CallID, secretmatch.ScreenCallIDFrom(ctx))
	if callID == "" {
		callID = uuid.NewString()
	}
	req.Debug.CallID = callID
	ctx = secretmatch.WithScreenCallID(ctx, callID)
	capStart := time.Now()
	if sel != nil {
		// Standing refusals avoid repeated calls.
		if refused := c.refusals.Err(sel.ProviderID, sel.Model); refused != nil {
			return nil, sel, refused
		}
		if err := c.capacity.Await(ctx, sel.ProviderID, sel.Model); err != nil {
			emitCapacityWait(ctx, "foreground", capStart, true)
			return nil, sel, err
		}
	}
	ch, adm, err := c.openOnce(ctx, sel, req)
	if adm == nil || !adm.singleFlight {
		emitCapacityWait(ctx, "foreground", capStart, err != nil && !adm.Dispatched())
	}
	for hold := 0; adm.Dispatched() && c.capacity.ShouldHold(err, hold); hold++ {
		if holdErr := c.capacity.Hold(ctx, hold); holdErr != nil {
			if sel != nil {
				c.capacity.NoteFailure(sel.ProviderID, sel.Model, err)
			}
			return nil, sel, holdErr
		}
		ch, adm, err = c.openOnce(ctx, sel, req)
	}
	if err != nil {
		if sel != nil && adm.Dispatched() {
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
	return ch, sel, nil
}

// observeStreamOutcome records provider failures before exposing their chunks.
func (c *RoutingClient) observeStreamOutcome(ctx context.Context, sel *ModelSelection, ch <-chan modelcall.StreamChunk) <-chan modelcall.StreamChunk {
	out := make(chan modelcall.StreamChunk, 64)
	go func() {
		defer close(out)
		for chunk := range ch {
			if chunk.Err != nil {
				c.capacity.NoteFailure(sel.ProviderID, sel.Model, chunk.Err)
				c.refusals.Note(sel.ProviderID, sel.Model, chunk.Err)
			}
			if !modelcall.SendChunk(ctx, out, chunk) {
				modelcall.DrainStream(ch)
				return
			}
		}
	}()
	return out
}

func (c *RoutingClient) openOnce(ctx context.Context, sel *ModelSelection, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, *callAdmission, error) {
	if c.registry != nil && sel != nil {
		adm := c.foregroundAdmission(sel)
		primary, err := c.registry.StreamWithSelection(withCallAdmission(ctx, adm), sel, req)
		if err != nil {
			return nil, adm, stampSelection(fmt.Errorf("provider %s: %w", sel.ProviderID, err), sel)
		}
		return primary, adm, nil
	}
	if sel != nil {
		return nil, nil, &failure.ProviderNotConfiguredError{ProviderID: sel.ProviderID}
	}
	return nil, nil, fmt.Errorf("no AI provider available")
}

// stampSelection supplies missing provider and model identities.
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

func (c *RoutingClient) foregroundAdmission(sel *ModelSelection) *callAdmission {
	adm := &callAdmission{foreground: true}
	if c.plane == nil || c.registry == nil || sel == nil {
		return adm
	}
	provider, err := c.registry.Get(sel.ProviderID)
	if err != nil || !provider.Profile().UtilitySingleFlight {
		return adm
	}
	adm.plane = c.plane
	adm.capacityKey = modelcall.CapacityKey(provider)
	adm.singleFlight = true
	return adm
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
