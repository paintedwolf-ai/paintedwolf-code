package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
)

func TestContextFactsSurviveObservationMiddlewareWithoutInferringLocalityFromNames(t *testing.T) {
	for _, id := range []string{"custom-runtime", "ollama-hosted", "openai-compatible-company"} {
		for _, local := range []bool{false, true} {
			for _, logged := range []bool{false, true} {
				model := modelinfo.Entry{ID: "model", ContextLength: 16000}
				adapter := openaicompat.New(id, "http://fixture.invalid/v1", "", []modelinfo.Entry{model})
				profile := providerprofile.OpenAIVendor()
				if local {
					profile = providerprofile.LocalInference(profile)
				}
				adapter = adapter.WithProfile(profile)
				var provider modelcall.Provider = adapter
				if logged {
					provider = &loggingProvider{inner: provider}
				}
				registry := &Registry{}
				registry.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
					Providers: map[string]modelcall.Provider{id: provider},
					Entries:   map[string]CatalogEntry{id: {ID: id, Kind: "openai", Models: []modelinfo.Entry{model}}},
				}))
				if got := isLocalProvider(id, registry); got != local {
					t.Fatalf("provider ID or middleware changed locality: id=%s logged=%t local=%t got=%t", id, logged, local, got)
				}
				RecordContextWindow(registry, id, "model", 4000, "structured-overflow", true)
				entry, ok := ContextEntry(registry, id, "model")
				if !ok || entry.ContextLength != 4000 || entry.ContextLengthSource != "structured-overflow" {
					t.Fatalf("observation middleware hid the serving window: logged=%t entry=%+v", logged, entry)
				}
			}
		}
	}
	if isLocalProvider("ollama-arbitrary-id", nil) {
		t.Fatal("a name alone established provider locality")
	}
}
