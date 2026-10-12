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
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/llm/providers/bedrock"
	"github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestThinkingCapabilitiesRespectNativeTransport(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	for _, tc := range []struct {
		model   string
		profile providerprofile.Profile
		effort  string
		budget  bool
		disable bool
	}{
		{"@cf/zai-org/glm-5.3-flash", providerprofile.CloudflareWorkersAI(), "high", false, false},
		{"glm-5.3-flash:cloud", providerprofile.Ollama(), "max", false, false},
		{"gpt-oss:20b", providerprofile.Ollama(), "medium", false, false},
		{"qwen3:8b", providerprofile.Ollama(), "", false, true},
		{"claude-opus-4-6", providerprofile.Anthropic(), "max", false, true},
		{"claude-sonnet-4-5", providerprofile.Anthropic(), "", true, true},
		{"gemini-2.5-flash", providerprofile.VertexExpress(), "", true, true},
		{"gemini-2.5-flash", providerprofile.Gemini(), "medium", false, true},
		{"gemini-3-pro", providerprofile.VertexExpress(), "high", false, false},
	} {
		t.Run(tc.model+"/"+string(tc.profile.Discovery), func(t *testing.T) {
			c := modelcall.ResolveThinkingCapabilities(tc.profile, modelinfo.Entry{ID: tc.model}, tc.model)
			if c.State != "supported" || c.CanDisable != tc.disable || (c.Budget != nil) != tc.budget {
				t.Fatalf("wrong native capabilities: %+v", c)
			}
			if tc.effort != "" {
				if err := acceptsThinkingCapabilities(c, modelcall.ThinkingOverride{Mode: "fixed", Effort: tc.effort}); err != nil {
					t.Fatalf("native effort unavailable: %v", err)
				}
			}
		})
	}
}

func TestThinkingCapabilitiesConfigurationWinsDiscovery(t *testing.T) {
	entry := modelinfo.Entry{ID: "custom", ThinkStyle: "effort_levels", Thinking: &modelinfo.ThinkingCapabilities{State: "supported", Efforts: []string{"deliberate"}}, DiscoveredThinking: &modelinfo.ThinkingCapabilities{State: "supported", Efforts: []string{"high"}}}
	c := modelcall.ResolveThinkingCapabilities(providerprofile.OpenAI(), entry, entry.ID)
	if c.Source != "provider-config" || len(c.Efforts) != 1 || c.Efforts[0] != "deliberate" {
		t.Fatalf("custom declaration lost: %+v", c)
	}
	c.Efforts[0] = "mutated"
	if entry.Thinking.Efforts[0] != "deliberate" {
		t.Fatal("capability resolution mutated config")
	}
	entry.Thinking, entry.DiscoveredThinking = nil, nil
	if got := modelcall.ResolveThinkingCapabilities(providerprofile.OpenAI(), entry, entry.ID); got.State != "unknown" {
		t.Fatalf("invented model capabilities: %+v", got)
	}
}

func TestThinkingOverrideOutputHeadroom(t *testing.T) {
	budget := 4096
	entry := modelinfo.Entry{ID: "custom", ThinkStyle: "budget_tokens", Thinking: &modelinfo.ThinkingCapabilities{State: "supported", Budget: &modelinfo.ThinkingBudgetRange{Min: 1024}}}
	o := modelcall.ThinkingOverride{ProviderID: "p", Model: "custom", Mode: "fixed", BudgetTokens: &budget}
	for _, limit := range []int{128, 4096} {
		if err := validateThinkingRequest(providerprofile.Anthropic(), entry, o, modelcall.CompletionRequest{MaxTokens: limit}); err == nil {
			t.Fatalf("budget exceeded hard output limit %d", limit)
		}
	}
	if err := validateThinkingRequest(providerprofile.Anthropic(), entry, o, modelcall.CompletionRequest{MaxTokens: 8192}); err != nil {
		t.Fatalf("valid native budget rejected: %v", err)
	}
}

func TestAnthropicThinkingDiscoveryDrivesNewModelControls(t *testing.T) {
	raw := `{"id":"new-model","max_tokens":16384,"max_input_tokens":200000,"capabilities":{"thinking":{"supported":true,"types":{"adaptive":{"supported":true},"enabled":{"supported":false}}},"effort":{"supported":true,"low":{"supported":true},"high":{"supported":true},"xhigh":{"supported":false},"max":{"supported":true}}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[` + raw + `]}`))
	}))
	t.Cleanup(server.Close)
	entries, err := discovery.AnthropicModels(t.Context(), server.URL, "test-key", server.Client())
	if err != nil || len(entries) != 1 {
		t.Fatalf("discover metadata: entries=%v error=%v", entries, err)
	}
	entry := entries[0]
	got := modelcall.ResolveThinkingCapabilities(providerprofile.Anthropic(), entry, entry.ID)
	if got.Source != "provider-discovery" || len(got.Efforts) != 3 || got.Efforts[2] != "max" || got.CanDisable || got.Budget != nil {
		t.Fatalf("discovered capabilities lost: %+v", got)
	}
	if modelcall.ResolveModelThinking(providerprofile.Anthropic(), entry, entry.ID).Style != modelinfo.ThinkStyleAdaptive {
		t.Fatal("new model received legacy budget wire style")
	}
	if entry.MaxTokens != 16384 || entry.ContextLength != 200000 {
		t.Fatal("discovered limits lost")
	}
	entry.ThinkStyle = "budget_tokens"
	entry.Thinking = &modelinfo.ThinkingCapabilities{State: "supported", Budget: &modelinfo.ThinkingBudgetRange{Min: 2048}}
	got = modelcall.ResolveThinkingCapabilities(providerprofile.Anthropic(), entry, entry.ID)
	if got.Source != "provider-config" || got.Budget == nil || len(got.Efforts) != 0 {
		t.Fatal("explicit configuration did not override discovery")
	}
}

func TestThinkingCapabilitiesRespectModelOutputCeiling(t *testing.T) {
	entry := modelinfo.Entry{ID: "budget-model", MaxTokens: 8192, ThinkStyle: "budget_tokens", Thinking: &modelinfo.ThinkingCapabilities{State: "supported", Budget: &modelinfo.ThinkingBudgetRange{Min: 1024}}}
	c := modelcall.ResolveThinkingCapabilities(providerprofile.Anthropic(), entry, entry.ID)
	if c.Budget == nil || c.Budget.Max != 8191 {
		t.Fatalf("model ceiling missing from settings range: %+v", c)
	}
	entry.MaxTokens = 512
	c = modelcall.ResolveThinkingCapabilities(providerprofile.Anthropic(), entry, entry.ID)
	if c.Budget != nil {
		t.Fatal("offered a budget that cannot leave answer space")
	}
}

func TestThinkingEvidenceControlsEveryAdapterWire(t *testing.T) {
	withThinkingRules(t, bundledThinkingRules(t))
	profiles := map[string]providerprofile.Profile{
		"openai": providerprofile.OpenAIVendor(), "openai-compatible": providerprofile.OpenAI(),
		"azure": providerprofile.Azure(), "vertex": providerprofile.Vertex(),
		"fireworks": providerprofile.Fireworks(), "together": providerprofile.Together(),
		"gemini": providerprofile.Gemini(), "openrouter": providerprofile.Openrouter(),
		"lmstudio": providerprofile.Lmstudio(), "omlx": providerprofile.Omlx(),
		"litellm-proxy": providerprofile.Litellm(), "cloudflare-workers-ai": providerprofile.CloudflareWorkersAI(),
	}
	for _, state := range []string{"unsupported", "unknown"} {
		for name, profile := range profiles {
			t.Run(name+"/"+state, func(t *testing.T) {
				entry := modelinfo.Entry{ID: "qwen3", DiscoveredThinking: &modelinfo.ThinkingCapabilities{State: state}}
				p := openaicompat.New(name, "http://unused.invalid", "", nil).WithProfile(profile)
				p = hydrateProviderModels(p, []modelinfo.Entry{entry}).(*openaicompat.Provider)
				for _, stream := range []bool{false, true} {
					body, err := p.Prepare(nonReasoningRequest(entry.ID), stream)
					testutil.FailErr(t, "prepare compatible request", err)
					assertNoThinkingFields(t, body)
				}
			})
		}
		for _, name := range []string{"ollama", "anthropic", "vertex-express"} {
			t.Run(name+"/"+state, func(t *testing.T) {
				entry := modelinfo.Entry{ID: "qwen3", ContextLength: 65536, DiscoveredThinking: &modelinfo.ThinkingCapabilities{State: state}}
				for _, stream := range []bool{false, true} {
					req := nonReasoningRequest(entry.ID)
					var body any
					switch name {
					case "ollama":
						p := hydrateProviderModels(ollama.New(name, "http://unused.invalid", "", nil), []modelinfo.Entry{entry}).(*ollama.Provider)
						prepared, _, err := p.Prepare(t.Context(), req, stream)
						testutil.FailErr(t, "prepare native request", err)
						body = prepared
					case "anthropic":
						p := hydrateProviderModels(anthropic.New(name, "http://unused.invalid", "", nil), []modelinfo.Entry{entry}).(*anthropic.Provider)
						body = p.Prepare(req, stream)
					case "vertex-express":
						p := hydrateProviderModels(vertexexpress.New(name, "http://unused.invalid", "", nil), []modelinfo.Entry{entry}).(*vertexexpress.Provider)
						body = p.Prepare(req)
					}
					encoded, err := json.Marshal(body)
					testutil.FailErr(t, "encode native request", err)
					assertNoThinkingFields(t, encoded)
					if req.AttemptBudget.CanReduceReasoning {
						t.Fatal("non-reasoner offered reasoning recovery")
					}
				}
			})
		}
	}
	p := bedrock.New("bedrock", "us-east-1", "", nil)
	if got := modelcall.ResolveThinkingCapabilities(p.Profile(), modelinfo.Entry{}, "model"); got.State != "unsupported" {
		t.Fatalf("Bedrock advertised unsupported controls: %+v", got)
	}
}

func nonReasoningRequest(model string) modelcall.CompletionRequest {
	return modelcall.CompletionRequest{Model: model, Think: modelcall.ThinkHigh,
		Messages:      []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
		AttemptBudget: &modelcall.CompletionBudget{},
	}
}

func assertNoThinkingFields(t *testing.T, body []byte) {
	t.Helper()
	var fields map[string]any
	testutil.FailErr(t, "decode request fields", json.Unmarshal(body, &fields))
	for _, name := range []string{"think", "thinking", "reasoning", "reasoning_effort", "output_config"} {
		if _, present := fields[name]; present {
			t.Errorf("unsupported control %s in %s", name, body)
		}
	}
	if config, ok := fields["generationConfig"].(map[string]any); ok {
		if _, present := config["thinkingConfig"]; present {
			t.Errorf("unsupported thinkingConfig in %s", body)
		}
	}
}

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
