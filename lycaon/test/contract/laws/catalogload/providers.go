package catalogload

import (
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/test/contract/laws"
)

// shippedProvider is the ship-catalog shape of one first-party provider.
// Empty base URL, label and reasoning wire are not asserted.
type shippedProvider struct {
	id, kind, baseURL, apiKeyEnv, label, ambientAuth, reasoningWire string
}

var shippedProviders = []shippedProvider{
	{id: "anthropic", kind: "anthropic", baseURL: "https://api.anthropic.com/v1", apiKeyEnv: "ANTHROPIC_API_KEY", label: "Anthropic"},
	{id: "together", kind: "together", baseURL: "https://api.together.xyz/v1", apiKeyEnv: "TOGETHER_API_KEY"},
	{id: "doubleword", kind: "doubleword", baseURL: "https://api.doubleword.ai/v1", apiKeyEnv: "DOUBLEWORD_API_KEY", label: "Doubleword"},
	{id: "azure", kind: "azure", apiKeyEnv: "AZURE_OPENAI_API_KEY"},
	// Bedrock takes a pasteable key and names the AWS SDK chain as the
	// alternative; Vertex has no pasteable credential for the same models.
	{id: "bedrock", kind: "bedrock", apiKeyEnv: "AWS_BEARER_TOKEN_BEDROCK", ambientAuth: "aws-sdk-chain"},
	{id: "vertex", kind: "vertex", ambientAuth: "google-adc"},
	{id: "vertex-express", kind: "vertex-express", baseURL: "https://aiplatform.googleapis.com/v1", apiKeyEnv: "GOOGLE_API_KEY"},
	{id: "cerebras", kind: "cerebras", baseURL: "https://api.cerebras.ai/v1", apiKeyEnv: "CEREBRAS_API_KEY", label: "Cerebras", reasoningWire: string(providerprofile.ReasoningWireContent)},
	{id: "cloudflare-workers-ai", kind: "cloudflare-workers-ai", baseURL: "https://api.cloudflare.com/client/v4/accounts/YOUR_ACCOUNT_ID/ai/v1", apiKeyEnv: "CLOUDFLARE_API_TOKEN", label: "Cloudflare Workers AI"},
	{id: "deepinfra", kind: "deepinfra", baseURL: "https://api.deepinfra.com/v1/openai", apiKeyEnv: "DEEPINFRA_API_KEY", label: "DeepInfra", reasoningWire: string(providerprofile.ReasoningWireContent)},
	{id: "gemini", kind: "gemini", baseURL: "https://generativelanguage.googleapis.com/v1beta/openai", apiKeyEnv: "GEMINI_API_KEY"},
	{id: "mistral", kind: "mistral", baseURL: "https://api.mistral.ai/v1", apiKeyEnv: "MISTRAL_API_KEY", label: "Mistral"},
}

func providerConfig(r *laws.Run) *llm.ProviderConfig {
	return laws.Part(r.Corpus, "provider config", llm.LoadProviderConfig)
}

var providers = []laws.Rule{{
	// The first-party providers ship as kind templates: the declared shape
	// and no models, which come from live discovery once configured.
	Name:    "bundled-providers-ship-kind-templates",
	Anchors: []laws.Anchor{laws.Func("internal/llm", "LoadProviderConfig")},
	Check: func(r *laws.Run) {
		byID := map[string]llm.ProviderEntry{}
		for _, p := range providerConfig(r).Providers {
			byID[p.ID] = p
		}
		for _, want := range shippedProviders {
			p, ok := byID[want.id]
			if !ok {
				r.Report("the ship catalog has no provider %s", want.id)
				continue
			}
			got := shippedProvider{id: p.ID, kind: p.Kind, baseURL: p.BaseURL, apiKeyEnv: p.APIKeyEnv, label: p.Label,
				ambientAuth: p.AmbientAuth, reasoningWire: string(p.ReasoningWire)}
			if want.baseURL == "" {
				got.baseURL = ""
			}
			if want.label == "" {
				got.label = ""
			}
			if want.reasoningWire == "" {
				got.reasoningWire = ""
			}
			if got != want {
				r.Report("provider %s ships %+v, want %+v", want.id, got, want)
			}
			if len(p.Models) != 0 {
				r.Report("provider %s ships %d models; models come from live discovery", want.id, len(p.Models))
			}
		}
	},
}, {
	// Every provider declares a valid retry policy, with capacity statuses
	// on the capacity policy rather than the throttle list.
	Name:    "bundled-providers-declare-http-retry",
	Anchors: []laws.Anchor{laws.Func("internal/llm/providerretry", "ValidateHTTPRetry")},
	Check: func(r *laws.Run) {
		cfg := providerConfig(r)
		r.Expect(len(cfg.Providers), "shipped providers")
		for _, p := range cfg.Providers {
			if p.HTTPRetry.IsZero() || p.HTTPRetry.Capacity == nil {
				r.Report("provider %s needs http_retry with a capacity policy", p.ID)
				continue
			}
			if err := providerretry.ValidateHTTPRetry(p.HTTPRetry); err != nil {
				r.Report("provider %s: %v", p.ID, err)
			}
			for _, status := range p.HTTPRetry.Statuses {
				if status == 503 || status == 529 {
					r.Report("provider %s: HTTP %d belongs on http_retry.capacity", p.ID, status)
				}
			}
		}
		if _, err := cfg.Capacity.Policy(); err != nil {
			r.Report("provider_capacity: %v", err)
		}
	},
}}
