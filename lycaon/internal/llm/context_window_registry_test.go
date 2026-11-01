package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
				adapter.RecordContextWindow("model", 4000, "structured-overflow", true)
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

func TestContextMetadataUsesObservedFactsWithoutProbingOrCrossingAdapterIdentity(t *testing.T) {
	for _, logged := range []bool{false, true} {
		var requests atomic.Uint32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			requests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if request.URL.Path == "/v1/models" {
				_, _ = w.Write([]byte(`{"data":[{"id":"model","max_model_len":8192}]}`))
				return
			}
			http.NotFound(w, request)
		}))
		t.Cleanup(server.Close)
		adapter := openaicompat.New("runtime", server.URL+"/v1", "", []modelinfo.Entry{{ID: "model"}}).
			WithProfile(providerprofile.LocalInference(providerprofile.OpenAI()))
		registry := &Registry{}
		publish := func(adapter *openaicompat.Provider, model modelinfo.Entry) {
			var provider modelcall.Provider = adapter
			if logged {
				provider = &loggingProvider{inner: provider}
			}
			registry.snapshot.Store(newProviderRegistrySnapshot(providerRegistrySnapshotInput{
				Providers: map[string]modelcall.Provider{"runtime": provider},
				Entries:   map[string]CatalogEntry{"runtime": {ID: "runtime", Kind: "openai-compatible", Models: []modelinfo.Entry{model}}},
			}))
		}
		publish(adapter, modelinfo.Entry{ID: "model"})
		if window := registry.ContextLength("runtime", "model"); window != 0 || requests.Load() != 0 {
			t.Fatalf("metadata lookup started work or invented a window: window=%d requests=%d", window, requests.Load())
		}
		if window := adapter.ContextLength("runtime", "model"); window != 0 || requests.Load() != 0 {
			t.Fatal("direct adapter metadata performed HTTP discovery")
		}
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if window := adapter.ContextLimit(canceled, "model"); window != 0 {
			t.Fatal("canceled discovery invented a serving window")
		}
		if window := adapter.ContextLimit(t.Context(), "model"); window != 8192 {
			t.Fatalf("request preparation did not discover the serving window: %d", window)
		}
		observed := requests.Load()
		for range 3 {
			if window := registry.ContextLength("runtime", "model"); window != 8192 || requests.Load() != observed {
				t.Fatalf("cached metadata lookup changed its work bound: window=%d requests=%d", window, requests.Load())
			}
		}
		replacement := openaicompat.New("runtime", server.URL+"/replacement/v1", "", []modelinfo.Entry{{ID: "model", ContextLength: 32000}})
		publish(replacement, modelinfo.Entry{ID: "model", ContextLength: 32000})
		adapter.RecordContextWindow("model", 2048, "structured-overflow", true)
		if window := registry.ContextLength("runtime", "model"); window != 32000 {
			t.Fatalf("old in-flight adapter changed the replacement's context: %d", window)
		}
		server.Close()
	}
}
