package llm

import (
	"context"
	"path/filepath"
	"testing"

	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	bedrockprovider "github.com/lycaon/lycaon/internal/llm/providers/bedrock"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	vertexexpressprovider "github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
)

func TestNewProviderForEntryDispatch(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		wantType  string
		wantDisco providerprofile.DiscoveryStrategy
		wantThink modelinfo.ThinkStyle
	}{
		{"ollama native", "ollama", "*ollama.Provider", providerprofile.DiscoveryOllama, modelinfo.ThinkStyleBooleanThink},
		{"fireworks discovery", "fireworks", "*openaicompat.Provider", providerprofile.DiscoveryFireworks, modelinfo.ThinkStyleEffortLevels},
		{"together profile discovery", "together", "*openaicompat.Provider", providerprofile.DiscoveryConfigured, modelinfo.ThinkStyleReasoningToggle},
		{"gemini native discovery", "gemini", "*openaicompat.Provider", providerprofile.DiscoveryGemini, modelinfo.ThinkStyleEffortLevels},
		{"openrouter profile discovery", "openrouter", "*openaicompat.Provider", providerprofile.DiscoveryConfigured, modelinfo.ThinkStyleReasoningObject},
		{"cloudflare workers ai catalog", "cloudflare-workers-ai", "*openaicompat.CloudflareProvider", providerprofile.DiscoveryCloudflareWorkersAI, modelinfo.ThinkStyleEffortLevels},
		{"lmstudio native discovery", "lmstudio", "*openaicompat.Provider", providerprofile.DiscoveryLMStudio, modelinfo.ThinkStyleEffortLevels},
		{"omlx native discovery", "omlx", "*openaicompat.Provider", providerprofile.DiscoveryOMLX, modelinfo.ThinkStyleEffortLevels},
		{"litellm proxy native discovery", "litellm-proxy", "*openaicompat.Provider", providerprofile.DiscoveryLiteLLM, modelinfo.ThinkStyleEffortLevels},
		{"anthropic native", "anthropic", "*anthropic.Provider", providerprofile.DiscoveryAnthropic, modelinfo.ThinkStyleBudgetTokens},
		{"azure deployments", "azure", "*openaicompat.Provider", providerprofile.DiscoveryAzure, modelinfo.ThinkStyleEffortLevels},
		{"bedrock inference profile discovery", "bedrock", "*bedrock.Provider", providerprofile.DiscoveryBedrock, modelinfo.ThinkStyleNone},
		{"vertex model garden discovery", "vertex", "*openaicompat.Provider", providerprofile.DiscoveryVertex, modelinfo.ThinkStyleEffortLevels},
		{"vertex express native", "vertex-express", "*vertexexpress.Provider", providerprofile.DiscoveryVertexExpress, modelinfo.ThinkStyleBudgetTokens},
		{"openai official", "openai", "*openaicompat.Provider", providerprofile.DiscoveryConfigured, modelinfo.ThinkStyleEffortLevels},
		{"openai-compatible default", "openai-compatible", "*openaicompat.Provider", providerprofile.DiscoveryConfigured, modelinfo.ThinkStyleEffortLevels},
		{"unknown kind falls back to openai", "totally-new", "*openaicompat.Provider", providerprofile.DiscoveryConfigured, modelinfo.ThinkStyleEffortLevels},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := CatalogEntry{ID: "p", Kind: tc.kind, BaseURL: "http://localhost:1234/v1"}
			if tc.kind == "cloudflare-workers-ai" {
				entry.BaseURL = "https://api.cloudflare.com/client/v4/accounts/acct-test/ai/v1"
			}
			reg, err := NewRegistry(t.Context(),
				mustTestProviderCatalog(t, entry),
				providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")),
			)
			if err != nil {
				t.Fatalf("NewRegistry: %v", err)
			}
			p, err := newProviderForEntry(context.Background(), entry, "key", reg.providerRetryPolicy(entry), reg.cloudflareUsage, reg.discoveryClient)
			if err != nil {
				t.Fatalf("newProviderForEntry: %v", err)
			}
			if got := providerTypeName(p); got != tc.wantType {
				t.Fatalf("driver type = %s, want %s", got, tc.wantType)
			}
			profile := p.Profile()
			if profile.Discovery != tc.wantDisco {
				t.Errorf("discovery = %q, want %q", profile.Discovery, tc.wantDisco)
			}
			if profile.Thinking != tc.wantThink {
				t.Errorf("thinking = %q, want %q", profile.Thinking, tc.wantThink)
			}
			if tc.kind == "together" {
				if len(profile.DiscoveryProfile.IncludeWireTypes) != 1 {
					t.Fatalf("together profile filters = %v", profile.DiscoveryProfile.IncludeWireTypes)
				}
				if !profile.DiscoveryProfile.RequirePositiveTokenPrice {
					t.Fatal("unpriced models should be uncallable")
				}
			}
		})
	}
}

func providerTypeName(p modelcall.Provider) string {
	switch p.(type) {
	case *ollamaprovider.Provider:
		return "*ollama.Provider"
	case *openaicompat.Provider:
		return "*openaicompat.Provider"
	case *anthropicprovider.Provider:
		return "*anthropic.Provider"
	case *bedrockprovider.Provider:
		return "*bedrock.Provider"
	case *vertexexpressprovider.Provider:
		return "*vertexexpress.Provider"
	case *openaicompat.CloudflareProvider:
		return "*openaicompat.CloudflareProvider"
	default:
		return "unknown"
	}
}

func TestToolCallSupportHelpers(t *testing.T) {
	cases := []struct {
		support       providerprofile.ToolCallSupport
		wantSupported bool
	}{
		{providerprofile.ToolCallsNative, true},
		{providerprofile.ToolCallsNone, false},
	}
	for _, tc := range cases {
		if got := tc.support.SupportsToolCalls(); got != tc.wantSupported {
			t.Errorf("%q.SupportsToolCalls() = %v, want %v", tc.support, got, tc.wantSupported)
		}
	}
}

func TestRoundTripsToolCallID(t *testing.T) {
	// The zero value is host-managed: the default drivers keep tool-call identity on
	// the host side and only an AI provider that explicitly opts into wire round-trip
	// echoes its own token.
	if providerprofile.OpenAI().RoundTripsToolCallID() {
		t.Error("openAIDriverProfile should default to host-managed tool-call ids")
	}
	if providerprofile.Ollama().RoundTripsToolCallID() {
		t.Error("ollamaDriverProfile should default to host-managed tool-call ids")
	}
	profile := providerprofile.OpenAI()
	profile.ToolCallID = providerprofile.ToolCallIDWireRoundTrip
	if !profile.RoundTripsToolCallID() {
		t.Error("ToolCallIDWireRoundTrip should round-trip the provider token")
	}
}
