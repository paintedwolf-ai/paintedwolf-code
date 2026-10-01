package llm

import (
	"context"
	"fmt"
	"net/http"

	"github.com/lycaon/lycaon/internal/catalogruntime"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	bedrockprovider "github.com/lycaon/lycaon/internal/llm/providers/bedrock"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	vertexexpressprovider "github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
)

type providerBuild struct {
	cloudflareUsage *openaicompat.CloudflareUsageCache
	discoveryClient *http.Client
	entry           CatalogEntry
	apiKey          string
	resolved        ResolvedProvider
}

// driverFactories selects a constructor by provider kind.
var driverFactories = catalogruntime.NewFactorySet(
	map[string]catalogruntime.Factory[providerBuild, modelcall.Provider]{
		"ollama": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return ollamaprovider.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models), nil
		},
		"anthropic": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return anthropicprovider.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models), nil
		},
		"openai": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return openaicompat.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models).
				WithProfile(providerprofile.OpenAIVendor()).
				WithHTTPProfile(openaicompat.OpenAIHTTPProfile()), nil
		},
		"openai-compatible": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return openaicompat.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models).
				WithProfile(providerprofile.LocalInference(providerprofile.OpenAI())), nil
		},
		"azure": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return openaicompat.New(build.entry.ID, build.resolved.ChatBaseURL, build.apiKey, build.entry.Models).
				WithProfile(providerprofile.Azure()).
				WithHTTPProfile(openaicompat.AzureHTTPProfile()), nil
		},
		"bedrock": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			// A blank key selects ambient credentials.
			return bedrockprovider.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models), nil
		},
		"vertex": func(ctx context.Context, build providerBuild) (modelcall.Provider, error) {
			return openaicompat.NewVertex(ctx, build.entry.ID, build.entry.Models), nil
		},
		"vertex-express": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			// Express mode uses a key on the global endpoint.
			return vertexexpressprovider.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models), nil
		},
		"fireworks": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return openaicompat.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models).
				WithProfile(providerprofile.Fireworks()), nil
		},
		"together": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return openaicompat.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models).
				WithProfile(providerprofile.Together()), nil
		},
		"gemini": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return openaicompat.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models).
				WithProfile(providerprofile.Gemini()), nil
		},
		"openrouter": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return openaicompat.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models).
				WithProfile(providerprofile.Openrouter()).
				WithExtraHeaders(OpenRouterRankingHeaders()), nil
		},
		"lmstudio": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return openaicompat.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models).
				WithProfile(providerprofile.Lmstudio()), nil
		},
		"omlx": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return openaicompat.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models).
				WithProfile(providerprofile.Omlx()), nil
		},
		"litellm-proxy": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return openaicompat.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models).
				WithProfile(providerprofile.Litellm()), nil
		},
		"cloudflare-workers-ai": func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
			return openaicompat.NewCloudflare(
				build.entry.ID, build.entry.BaseURL, build.entry.Models, build.apiKey, build.cloudflareUsage, build.discoveryClient,
			), nil
		},
	},
	func(_ context.Context, build providerBuild) (modelcall.Provider, error) {
		return openaicompat.New(build.entry.ID, build.entry.BaseURL, build.apiKey, build.entry.Models), nil
	},
)

func newProviderForEntry(ctx context.Context, entry CatalogEntry, apiKey string, retry providerretry.ProviderHTTPRetry, usage *openaicompat.CloudflareUsageCache, discoveryClient *http.Client) (modelcall.Provider, error) {
	resolved := ResolveProvider(entry, apiKey)
	p, err := driverFactories.Build(ctx, entry.Kind, providerBuild{cloudflareUsage: usage, discoveryClient: discoveryClient, entry: entry, apiKey: apiKey, resolved: resolved})
	if err != nil {
		return nil, err
	}
	if err := configureReasoningWire(p, entry.ReasoningWire); err != nil {
		return nil, fmt.Errorf("provider %q: %w", entry.ID, err)
	}
	return attachHTTPRetry(attachPromptCache(p, entry.PromptCache), retry), nil
}

// attachPromptCache gives a driver its provider kind's prompt-cache policy.
func attachPromptCache(p modelcall.Provider, policy providerprofile.PromptCachePolicy) modelcall.Provider {
	switch t := p.(type) {
	case *openaicompat.Provider:
		return t.WithPromptCache(policy)
	case *anthropicprovider.Provider:
		return t.WithPromptCache(policy)
	case *ollamaprovider.Provider:
		return t.WithPromptCache(policy)
	case *bedrockprovider.Provider:
		return t.WithPromptCache(policy)
	case *vertexexpressprovider.Provider:
		return t.WithPromptCache(policy)
	case *openaicompat.CloudflareProvider:
		return t.WithPromptCache(policy)
	default:
		return p
	}
}

func attachHTTPRetry(p modelcall.Provider, policy providerretry.ProviderHTTPRetry) modelcall.Provider {
	if policy.IsZero() {
		return p
	}
	switch t := p.(type) {
	case *openaicompat.Provider:
		return t.WithHTTPRetry(policy)
	case *anthropicprovider.Provider:
		return t.WithHTTPRetry(policy)
	case *ollamaprovider.Provider:
		return t.WithHTTPRetry(policy)
	case *bedrockprovider.Provider:
		return t.WithHTTPRetry(policy)
	case *vertexexpressprovider.Provider:
		return t.WithHTTPRetry(policy)
	case *openaicompat.CloudflareProvider:
		return t.WithHTTPRetry(policy)
	default:
		return p
	}
}

// OpenRouterRankingHeaders are optional OpenRouter request headers for app attribution.
func OpenRouterRankingHeaders() map[string]string {
	return map[string]string{
		"HTTP-Referer":       "https://paintedwolf.ai/",
		"X-OpenRouter-Title": "Painted Wolf Code",
	}
}

// LocalReasoningWire preserves an explicit device choice without pinning kind defaults.
func (e CatalogEntry) LocalReasoningWire() providerprofile.ReasoningWireStyle {
	if e.ReasoningWireOverride {
		return e.ReasoningWire
	}
	return ""
}

func configureReasoningWire(p modelcall.Provider, style providerprofile.ReasoningWireStyle) error {
	if err := style.Validate(); err != nil || style == "" {
		return err
	}
	switch typed := p.(type) {
	case *openaicompat.Provider:
		typed.WithReasoningWire(style)
	case *openaicompat.CloudflareProvider:
		typed.WithReasoningWire(style)
	default:
		return fmt.Errorf("reasoning_wire is only supported by chat-completions transports")
	}
	return nil
}
