package llm

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	nativegenprovider "github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestNativegenFactoryRequiresHostRuntimeForExecution(t *testing.T) {
	entry := CatalogEntry{ID: "nativegen", Kind: "nativegen", Models: []modelinfo.Entry{nativegenprovider.DefaultModelEntry()}}
	provider, err := driverFactories.Build(context.Background(), entry.Kind, providerBuild{entry: entry})
	if err != nil {
		t.Fatal(err)
	}
	local, ok := provider.(*nativegenprovider.Provider)
	if !ok {
		t.Fatalf("provider = %T", provider)
	}
	if _, err := local.Stream(context.Background(), modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}}}); err == nil {
		t.Fatal("started without a host runtime")
	}
}

func TestNativegenRegistryRebuildRetainsHostRuntime(t *testing.T) {
	runtime := nativegenprovider.NewRuntime(nativegenprovider.RuntimeConfig{})
	if err := runtime.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	entry := CatalogEntry{ID: "nativegen", Kind: "nativegen", Models: []modelinfo.Entry{nativegenprovider.DefaultModelEntry()}}
	registry, err := NewRegistry(t.Context(), mustTestProviderCatalog(t, entry), nil, runtime)
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	t.Cleanup(func() { _ = registry.discovery.Close(context.Background()) })
	request := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}}}
	for revision := range 2 {
		provider := registry.snapshot.Load().providers[entry.ID]
		if provider == nil {
			t.Fatalf("revision %d omitted local provider", revision)
		}
		if _, err := provider.Stream(t.Context(), request); !errors.Is(err, context.Canceled) {
			t.Fatalf("revision %d replaced the closed host runtime: %v", revision, err)
		}
		if err := registry.Reload(t.Context()); err != nil {
			t.Fatalf("reload registry: %v", err)
		}
	}
}
