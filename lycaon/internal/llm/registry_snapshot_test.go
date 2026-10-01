package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
)

func TestRegistrySnapshotRejectsStaleConnectivityPublication(t *testing.T) {
	registry := newEmptyRegistry()
	oldProvider := openaicompat.New("provider", "https://old.example/v1", "old-key", []modelinfo.Entry{{ID: "old"}})
	oldSnapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers:        map[string]modelcall.Provider{"provider": oldProvider},
		Entries:          map[string]CatalogEntry{"provider": {ID: "provider", BaseURL: "https://old.example/v1"}},
		CredentialValues: map[string]string{"provider": "old-key"},
		CredentialStored: map[string]bool{"provider": true},
		Configured:       map[string]bool{"provider": true},
	})
	registry.snapshot.Store(oldSnapshot)

	newProvider := openaicompat.New("provider", "https://new.example/v1", "new-key", []modelinfo.Entry{{ID: "new"}})
	newSnapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers:        map[string]modelcall.Provider{"provider": newProvider},
		Entries:          map[string]CatalogEntry{"provider": {ID: "provider", BaseURL: "https://new.example/v1"}},
		CredentialValues: map[string]string{"provider": "new-key"},
		CredentialStored: map[string]bool{"provider": true},
		Configured:       map[string]bool{"provider": true},
	})
	registry.snapshot.Store(newSnapshot)

	registry.publishConnectivityResult(oldSnapshot, "provider", []modelinfo.Entry{{ID: "stale"}})
	current := registry.snapshot.Load()
	if current != newSnapshot {
		t.Fatal("a stale connectivity result replaced the current provider generation")
	}
	if _, exists := current.observations["provider"]; exists {
		t.Fatal("a stale connectivity result authenticated the current credential")
	}
	if got := current.providers["provider"].Models(); len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("current provider models = %+v, want new generation unchanged", got)
	}
}

func TestDiscoveryCacheKeySeparatesProviderGenerations(t *testing.T) {
	profile := providerprofile.Default()
	oldKey := discoveryCacheKey("provider", profile, "https://old.example/v1", "key-a")
	for name, key := range map[string]string{
		"endpoint":   discoveryCacheKey("provider", profile, "https://new.example/v1", "key-a"),
		"credential": discoveryCacheKey("provider", profile, "https://old.example/v1", "key-b"),
	} {
		if key == oldKey {
			t.Fatalf("%s change reused the prior discovery cache generation", name)
		}
	}
}

func TestModelEntryCloneOwnsNestedCapabilityEvidence(t *testing.T) {
	original := []modelinfo.Entry{{
		ID: "model",
		Capabilities: modelinfo.ModelCapabilities{
			Tools: modelinfo.CapabilityEvidence{State: modelinfo.CapabilitySupported, Sources: []string{"catalog"}},
		},
	}}
	cloned := modelinfo.CloneEntries(original)
	cloned[0].Capabilities.Tools.Sources[0] = "discovery"
	if got := original[0].Capabilities.Tools.Sources[0]; got != "catalog" {
		t.Fatalf("source mutation escaped clone: %q", got)
	}
}
