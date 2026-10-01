package llm

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
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
