package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
)

func TestHydrateProviderModelsDoesNotMutatePublishedProvider(t *testing.T) {
	original := openaicompat.New("openai", "https://example.invalid/v1", "key", []modelinfo.Entry{{ID: "old"}})
	hydrated := hydrateProviderModels(original, []modelinfo.Entry{{ID: "new"}})

	if got := original.Models(); len(got) != 1 || got[0].ID != "old" {
		t.Fatalf("published provider mutated in place: %+v", got)
	}
	if hydrated == original {
		t.Fatal("hydration returned the published provider object")
	}
	if got := hydrated.Models(); len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("hydrated provider models = %+v", got)
	}
}

func TestHydrateProviderModelsCanPublishAnEmptyCatalog(t *testing.T) {
	original := openaicompat.New("openai", "https://example.invalid/v1", "key", []modelinfo.Entry{{ID: "stale"}})
	hydrated := hydrateProviderModels(original, nil)
	if got := hydrated.Models(); len(got) != 0 {
		t.Fatalf("empty refresh retained stale models: %+v", got)
	}
	if got := original.Models(); len(got) != 1 || got[0].ID != "stale" {
		t.Fatalf("empty refresh mutated published provider: %+v", got)
	}
}

func TestHydrateProviderModelsInstallsNativeDiscoveryCapabilities(t *testing.T) {
	original := ollamaprovider.New("ollama", "http://localhost:11434/v1", "", []modelinfo.Entry{{ID: "vision-model"}})
	hydrated, ok := hydrateProviderModels(original, []modelinfo.Entry{{
		ID:           "vision-model",
		Capabilities: modelinfo.ModelCapabilities{Vision: modelinfo.Evidence(modelinfo.CapabilitySupported, "ollama-show")},
	}}).(*ollamaprovider.Provider)
	if !ok {
		t.Fatalf("hydrated provider type = %T", hydrated)
	}
	entry, ok := hydrated.ModelEntry("vision-model")
	if !ok || !modelinfo.Supported(entry.EffectiveCapabilities().Vision) {
		t.Fatalf("hydrated entry = %+v, %v", entry, ok)
	}
	originalEntry, _ := original.ModelEntry("vision-model")
	if modelinfo.Supported(originalEntry.EffectiveCapabilities().Vision) {
		t.Fatal("native hydration mutated the published provider")
	}
}
