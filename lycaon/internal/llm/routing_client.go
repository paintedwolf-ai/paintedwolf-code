package llm

import (
	"context"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/transcript"
)

// MockOnlyFromEnv reports whether development mock routing is active.
func MockOnlyFromEnv() bool {
	if !configdir.IsDevelopmentBuild() && !configdir.IsPerformanceBuild() {
		return false
	}
	return configdir.EnvTruthy(os.Getenv("LYCAON_LLM_MOCK"))
}

// ManualOnlyFromEnv reports whether manual routing is active.
func ManualOnlyFromEnv() bool {
	if !configdir.IsDevelopmentChannel() {
		return false
	}
	return configdir.EnvTruthy(os.Getenv("LYCAON_LLM_MANUAL"))
}

// RoutingClient resolves and invokes configured providers.
type RoutingClient struct {
	registry *Registry
	router   *StaticModelRouter
	mock     modelcall.LLMClient
	plane    *UtilityPlane
	capacity *CapacityGate
	refusals *providerretry.ModelRefusalGate
	resolve  func(ctx context.Context) (*ModelSelection, error)
}

// NewRoutingClient builds a client that routes coordinator prompts.
func NewRoutingClient(registry *Registry, router *StaticModelRouter, mock modelcall.LLMClient, plane *UtilityPlane, capacity *CapacityGate, refusals *providerretry.ModelRefusalGate, resolve func(context.Context) (*ModelSelection, error)) *RoutingClient {
	return &RoutingClient{
		registry: registry,
		router:   router,
		mock:     mock,
		plane:    plane,
		capacity: capacity,
		refusals: refusals,
		resolve:  resolve,
	}
}

func (c *RoutingClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	completion, _, err := c.completeViaStream(ctx, req)
	return completion, err
}

func (c *RoutingClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch, sel, err := c.openProviderStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return streamWithSelection(ctx, ch, sel), nil
}

func streamWithSelection(ctx context.Context, ch <-chan modelcall.StreamChunk, sel *ModelSelection) <-chan modelcall.StreamChunk {
	if ch == nil || sel == nil {
		return ch
	}
	out := make(chan modelcall.StreamChunk)
	go func() {
		defer close(out)
		for chunk := range ch {
			chunk.ProviderID = sel.ProviderID
			chunk.Model = sel.Model
			chunk.Fallback = sel.Fallback
			if !modelcall.SendChunk(ctx, out, chunk) {
				modelcall.DrainStream(ch)
				return
			}
		}
	}()
	return out
}

func (c *RoutingClient) completeViaStream(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, []string, error) {
	ch, sel, err := c.openProviderStream(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	completion, tokens, err := modelcall.CollectStream(ch)
	if err != nil {
		return nil, nil, err
	}
	if sel != nil {
		completion.ProviderID = sel.ProviderID
		completion.Model = sel.Model
		completion.Fallback = sel.Fallback
	}
	if !modelcall.CompletionHasPayload(completion) {
		return nil, nil, &failure.ProviderEmptyCompletionError{ProviderID: sel.ProviderID, Model: sel.Model}
	}
	return completion, tokens, nil
}

func (c *RoutingClient) openProviderStream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, *ModelSelection, error) {
	req.Messages = transcript.Project(req.Messages)
	// Environment settings control development routing.
	if MockOnlyFromEnv() || ManualOnlyFromEnv() {
		if c.mock == nil {
			return nil, nil, fmt.Errorf("injected LLM client forced but is nil")
		}
		// Manual routing keeps the caller's model context.
		sel := &ModelSelection{ProviderID: "mock", Model: "mock", Fallback: true}
		if !ManualOnlyFromEnv() {
			req.Model = "mock"
		}
		req.CaptureSources()
		notifyDispatch(ctx)
		ch, err := c.mock.Stream(ctx, req)
		return ch, sel, err
	}

	sel, err := c.resolveSelection(ctx)
	if err != nil {
		return nil, nil, err
	}
	if _, frozen := thinkingPolicyFromContext(ctx); !frozen && c.router != nil {
		policy, policyErr := c.router.effectivePolicy()
		if policyErr != nil {
			return nil, nil, policyErr
		}
		ctx = WithThinkingPolicy(ctx, policy)
	}
	return c.openWithCapacity(ctx, sel, req)
}

func (c *RoutingClient) occupyLocalStream(ctx context.Context, sel *ModelSelection, ch <-chan modelcall.StreamChunk) <-chan modelcall.StreamChunk {
	if c == nil || c.plane == nil || c.registry == nil || sel == nil || ch == nil {
		return ch
	}
	p, err := c.registry.Get(sel.ProviderID)
	if err != nil || p == nil || !p.Profile().UtilitySingleFlight {
		return ch
	}
	release := c.plane.holdCoordinator(sel.ProviderID)
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

func (c *RoutingClient) resolveSelection(ctx context.Context) (*ModelSelection, error) {
	if c.resolve == nil {
		return nil, nil
	}
	sel, err := c.resolve(ctx)
	if err != nil {
		return nil, err
	}
	if sel == nil {
		return nil, nil
	}
	return sel, nil
}

var _ modelcall.LLMClient = (*RoutingClient)(nil)
