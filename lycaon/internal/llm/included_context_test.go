package llm

import (
	"context"
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
