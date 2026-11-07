package llm

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	nativegenprovider "github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestNativegenFactoryRequiresHostRuntimeForExecution(t *testing.T) {
	entry := CatalogEntry{ID: "nativegen", Kind: "nativegen", Models: []modelinfo.Entry{nativegenprovider.DefaultModelEntry()}}
	provider, err := driverFactories.Build(context.Background(), entry.Kind, providerBuild{entry: entry})
	testutil.FailErr(t, "driverFactories.Build failed", err)
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
	registry, err := NewRegistry(t.Context(), mustTestProviderCatalog(t, entry), nil, &nativegenprovider.Availability{Runtime: runtime, Initial: nativegenprovider.Readiness{State: "unavailable"}})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	t.Cleanup(func() { _ = registry.discovery.Close(context.Background()) })
	request := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Hello"}}}
	for revision := range 2 {
		if status := registry.LocalGenerationStatus(); status.State != "stopped" {
			t.Fatalf("startup facts replaced live runtime status: %+v", status)
		}
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

func TestNativegenStartupFailureSurvivesCatalogReload(t *testing.T) {
	entry := CatalogEntry{ID: "nativegen", Kind: "nativegen", Models: []modelinfo.Entry{nativegenprovider.DefaultModelEntry()}}
	initial := nativegenprovider.Readiness{State: "insufficient_memory", Action: "none", Detail: "capacity unavailable"}
	registry, err := NewRegistry(t.Context(), mustTestProviderCatalog(t, entry), nil, &nativegenprovider.Availability{Initial: initial})
	if err != nil {
		t.Fatalf("create registry: %v", err)
	}
	t.Cleanup(func() { _ = registry.discovery.Close(context.Background()) })
	for range 2 {
		status := registry.LocalGenerationStatus()
		if status.State != string(initial.State) || status.RecoveryAction != string(initial.Action) || status.Detail != initial.Detail {
			t.Fatalf("startup facts lost: %+v", status)
		}
		if err := registry.RepairLocalGeneration(t.Context(), entry.ID); err == nil {
			t.Fatal("repair accepted without an allocated runtime")
		}
		if err := registry.Reload(t.Context()); err != nil {
			t.Fatalf("reload registry: %v", err)
		}
	}
}
