package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type canceledDiscoveryContext struct {
	context.Context
	checked bool
}

func (c *canceledDiscoveryContext) Err() error {
	c.checked = true
	return c.Context.Err()
}

func TestIncludedSelectionPreservesCallerCancellationAndExplicitProvider(t *testing.T) {
	local := &captureProvider{id: IncludedProviderID}
	external := &captureProvider{id: "external"}
	registry := newTestRegistry()
	testutil.FailErr(t, "register included provider", registry.Register(local))
	testutil.FailErr(t, "register external provider", registry.Register(external))
	publishIncludedModelFixture(t, registry, local)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !registry.IncludedModelAvailable(ctx) {
		t.Fatal("caller cancellation erased an admitted model snapshot")
	}
	registry.discovery.Invalidate(discoveryCacheKey(IncludedProviderID, local.Profile(), "", ""))
	probe := &canceledDiscoveryContext{Context: ctx}
	if registry.IncludedModelAvailable(probe) || !probe.checked {
		t.Fatal("missing model discovery ignored caller cancellation")
	}
	summarizer := &RegistrySummarizer{Registry: registry, Policy: NewInMemoryPolicyStore(ModelPolicy{
		Lite: ModelRef{ProviderID: external.ID(), Model: "lite"},
	})}
	probe.checked = false
	selected, model, err := summarizer.liteProvider(probe)
	testutil.FailErr(t, "resolve explicit utility provider", err)
	if selected.ID() != external.ID() || model != "lite" || !probe.checked {
		t.Fatal("utility resolution changed explicit selection or lost cancellation")
	}
	probe.checked = false
	_ = summarizer.SummaryBudget(probe)
	if !probe.checked {
		t.Fatal("summary budget resolution lost caller cancellation")
	}
	if local.last.Model != "" || external.last.Model != "" {
		t.Fatal("model policy resolution executed a model request")
	}
}

func TestRouterAvailabilityUsesRequestContext(t *testing.T) {
	type requestKey struct{}
	ctx := context.WithValue(t.Context(), requestKey{}, "request")
	calls := 0
	router := NewStaticModelRouter(NewInMemoryPolicyStore(ModelPolicy{})).WithLocalDefault(func(got context.Context) bool {
		calls++
		if got.Value(requestKey{}) != "request" {
			t.Fatal("availability lookup lost request attribution")
		}
		return true
	})
	selected, err := router.Lite(ctx)
	testutil.FailErr(t, "resolve included default", err)
	if selected == nil || selected.ProviderID != IncludedProviderID || calls != 1 {
		t.Fatal("included selection did not consult request-bound availability")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := router.Lite(canceled); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("canceled routing performed availability work")
	}
}
