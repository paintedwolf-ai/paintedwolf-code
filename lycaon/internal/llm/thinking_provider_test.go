package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

type thinkingCaptureProvider struct {
	calls []modelcall.CompletionRequest
}

func (p *thinkingCaptureProvider) ID() string { return "p" }
func (p *thinkingCaptureProvider) Models() []modelcall.ModelInfo {
	return []modelcall.ModelInfo{{ID: "m"}}
}
func (p *thinkingCaptureProvider) Profile() providerprofile.Profile {
	profile := providerprofile.OpenAI()
	profile.Discovery = providerprofile.DiscoveryNone
	return profile
}
func (p *thinkingCaptureProvider) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	p.calls = append(p.calls, req)
	return &modelcall.Completion{Content: "done"}, nil
}
func (p *thinkingCaptureProvider) Stream(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	p.calls = append(p.calls, req)
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: "done", Done: true}
	close(ch)
	return ch, nil
}

func TestRegistryThinkingBoundaryCompleteStreamAndApplication(t *testing.T) {
	capture := &thinkingCaptureProvider{}
	model := modelinfo.Entry{ID: "m", ThinkStyle: "effort_levels", Thinking: &modelinfo.ThinkingCapabilities{State: "supported", Efforts: []string{"high"}}}
	reg := newEmptyRegistry()
	reg.discovery = discovery.NewCache(nil)
	reg.discovery.Put(discoveryCacheKey("p", capture.Profile(), "", ""), []modelinfo.Entry{{ID: "m", Untyped: true}}, nil)
	snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers:  map[string]modelcall.Provider{"p": capture},
		Entries:    map[string]CatalogEntry{"p": {ID: "p", Kind: "openai-compatible", Models: []modelinfo.Entry{model}}},
		Configured: map[string]bool{"p": true},
	})
	reg.snapshot.Store(snapshot)
	policy := ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{{ProviderID: "p", Model: "m", Mode: "fixed", Effort: "high"}}}
	reg.thinkingPolicy.Store(NewInMemoryPolicyStore(policy))
	provider := reg.decorateProvider(snapshot, capture)
	_, err := provider.Complete(t.Context(), modelcall.CompletionRequest{Model: "m", Think: modelcall.ThinkOff, Composition: modelcall.CompositionHostUtility})
	testutil.FailErr(t, "complete with device override", err)
	stream, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Model: "m", Think: modelcall.ThinkLow})
	testutil.FailErr(t, "stream with device override", err)
	for range stream {
	}
	if len(capture.calls) != 2 {
		t.Fatalf("calls=%d", len(capture.calls))
	}
	for _, req := range capture.calls {
		if req.ThinkingOverride == nil || req.ThinkingOverride.Effort != "high" {
			t.Fatal("provider boundary lost fixed effort")
		}
	}
	ctx := WithThinkingPolicy(t.Context(), ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{{ProviderID: "p", Model: "m", Mode: "application"}}})
	_, err = provider.Complete(ctx, modelcall.CompletionRequest{Model: "m", Think: modelcall.ThinkLow})
	testutil.FailErr(t, "project application behavior", err)
	last := capture.calls[len(capture.calls)-1]
	if last.ThinkingOverride != nil || last.Think != modelcall.ThinkLow {
		t.Fatal("project did not restore application behavior")
	}
	// An explicit empty snapshot freezes absence of overrides for the turn.
	_, err = provider.Complete(WithThinkingPolicy(t.Context(), ModelPolicy{}), modelcall.CompletionRequest{Model: "m"})
	testutil.FailErr(t, "empty turn snapshot", err)
	if capture.calls[len(capture.calls)-1].ThinkingOverride != nil {
		t.Fatal("device policy leaked into frozen turn")
	}
}

func TestRegistryRejectsStaleThinkingBeforeSending(t *testing.T) {
	capture := &thinkingCaptureProvider{}
	reg := newEmptyRegistry()
	reg.discovery = discovery.NewCache(nil)
	snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{Providers: map[string]modelcall.Provider{"p": capture}})
	reg.snapshot.Store(snapshot)
	provider := reg.decorateProvider(snapshot, capture)
	ctx := WithThinkingPolicy(t.Context(), ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{{ProviderID: "p", Model: "gone", Mode: "fixed", Effort: "high"}}})
	for _, stream := range []bool{false, true} {
		var err error
		if stream {
			_, err = provider.Stream(ctx, modelcall.CompletionRequest{Model: "gone"})
		} else {
			_, err = provider.Complete(ctx, modelcall.CompletionRequest{Model: "gone"})
		}
		var rejected *ThinkingOverrideError
		if !errors.As(err, &rejected) {
			t.Fatalf("stream=%v: untyped failure: %v", stream, err)
		}
	}
	if len(capture.calls) != 0 {
		t.Fatal("invalid override spent an inference call")
	}
}
