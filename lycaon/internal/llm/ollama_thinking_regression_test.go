package llm

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOllamaDiscoveryToThinkingWireAndOverrideAdmission(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	var chats atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"devstral-small-2:latest"},{"name":"gemma4:e2b"},{"name":"gpt-oss:20b"}]}`))
		case "/api/show":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode metadata request: %v", err)
			}
			capabilities := []string{"completion", "vision", "tools"}
			if body["model"] != "devstral-small-2:latest" {
				capabilities = append(capabilities, "thinking")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": capabilities})
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/chat":
			chats.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode completion request: %v", err)
			}
			switch body["model"] {
			case "devstral-small-2:latest":
				if _, exists := body["think"]; exists {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"model does not support thinking"}`))
					return
				}
			case "gemma4:e2b":
				if body["think"] != true {
					t.Errorf("supported boolean control = %v", body["think"])
				}
			case "gpt-oss:20b":
				if body["think"] != "medium" {
					t.Errorf("supported level control = %v", body["think"])
				}
			}
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"done"},"done":true,"done_reason":"stop"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	discovered, err := discovery.OllamaModels(t.Context(), server.URL+"/v1", "", server.Client())
	testutil.FailErr(t, "discover local Ollama models", err)
	merged := mergeAssignableModels("ollama", nil, discovered, nil, nil, "", false)
	entry := CatalogEntry{ID: "ollama-1", Kind: "ollama", BaseURL: server.URL + "/v1"}
	p, err := newProviderForEntry(t.Context(), entry, "", providerretry.ProviderHTTPRetry{}, nil, server.Client())
	testutil.FailErr(t, "construct native adapter", err)
	p = hydrateProviderModels(p, merged.Models)
	reg := newEmptyRegistry()
	reg.discovery = discovery.NewCache(nil)
	reg.discovery.Put(discoveryCacheKey(entry.ID, p.Profile(), entry.BaseURL, ""), discovered, nil)
	snapshot := newProviderRegistrySnapshot(providerRegistrySnapshotInput{
		Providers: map[string]modelcall.Provider{entry.ID: p}, Entries: map[string]CatalogEntry{entry.ID: entry},
		Configured: map[string]bool{entry.ID: true},
	})
	reg.snapshot.Store(snapshot)
	provider := reg.decorateProvider(snapshot, p)
	for _, model := range merged.Models {
		for _, stream := range []bool{false, true} {
			req := modelcall.CompletionRequest{Model: model.ID, Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}}}
			if stream {
				chunks, err := provider.Stream(t.Context(), req)
				testutil.FailErr(t, "start coordinator stream", err)
				_, _, err = modelcall.CollectStream(chunks)
				testutil.FailErr(t, "collect coordinator response", err)
			} else {
				_, err := provider.Complete(t.Context(), req)
				testutil.FailErr(t, "complete coordinator request", err)
			}
		}
	}
	before := chats.Load()
	on := true
	ctx := WithThinkingPolicy(t.Context(), ModelPolicy{ThinkingOverrides: []modelcall.ThinkingOverride{{
		ProviderID: entry.ID, Model: "devstral-small-2:latest", Mode: "fixed", Enabled: &on,
	}}})
	_, err = provider.Stream(ctx, modelcall.CompletionRequest{Model: "devstral-small-2:latest"})
	var rejected *ThinkingOverrideError
	if !errors.As(err, &rejected) || chats.Load() != before {
		t.Fatalf("unsupported saved override reached inference: error=%v chats=%d", err, chats.Load()-before)
	}
	resolved, found := reg.thinkingModelEntry(t.Context(), entry.ID, "devstral-small-2:latest")
	if !found || !modelinfo.Refused(resolved.Capabilities.Reasoning) {
		t.Fatalf("negative evidence lost: %+v", resolved)
	}
	if got := modelcall.ResolveThinkingCapabilities(p.Profile(), resolved, resolved.ID); got.State != "unsupported" || got.Source != "provider-discovery" {
		t.Fatalf("settings disagree with native request: %+v", got)
	}
}
