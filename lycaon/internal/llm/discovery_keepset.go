package llm

import (
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// discoveryKeepSet records listing capabilities required by each strategy.
// The contract guard checks strategy coverage against this table.
var discoveryKeepSet = map[providerprofile.DiscoveryStrategy]string{
	providerprofile.DiscoveryConfigured: "Composable DiscoveryProfile over OpenAI-shaped /models " +
		"(Together type=chat + token pricing, OpenRouter output modalities, " +
		"OpenAI/generic id-only marked Untyped for fail-closed merge).",

	providerprofile.DiscoveryNone: "Explicit opt-out — no live listing.",

	providerprofile.DiscoveryOllama: "Native GET /api/tags exposes feature flags (completion vs embedding); " +
		"OpenAI-compat /v1/models is id-only and mixes embed models.",

	providerprofile.DiscoveryLMStudio: "Native GET /api/v0/models exposes type∈{llm,vlm,embeddings}; " +
		"OpenAI-compat /v1/models omits type.",

	providerprofile.DiscoveryOMLX: "Native GET /v1/models/status exposes model_type∈{llm,vlm,embedding,…}; " +
		"OpenAI-compat /v1/models is id-only and mixes non-chat engines.",

	providerprofile.DiscoveryLiteLLM: "Native GET /model/info exposes model_info.mode (chat vs embedding/rerank); " +
		"OpenAI-compat /models is id-only (404 fallback marks Untyped).",

	providerprofile.DiscoveryGemini: "Native GET {v1beta}/models exposes supportedGenerationMethods " +
		"(generateContent + createCachedContent keep-set); OpenAI-compat /models is id-only.",

	providerprofile.DiscoveryVertexExpress: "Gemini native GET {v1beta}/models validates the express API key " +
		"and exposes supportedGenerationMethods; the Vertex express transport has no list method.",

	providerprofile.DiscoveryFireworks: "Serverless catalog control plane — not the OpenAI-compat /models shape.",

	providerprofile.DiscoveryCloudflareWorkersAI: "Workers AI account catalog + neuron usage analytics; " +
		"not a plain OpenAI-compat /models list.",

	providerprofile.DiscoveryAnthropic: "Anthropic GET /v1/models on the Messages API host — not OpenAI /models.",

	providerprofile.DiscoveryAzure: "Azure Resource Manager deployment list (AAD); data-plane /models " +
		"does not expose deployment control-plane rows.",

	providerprofile.DiscoveryBedrock: "Bedrock ListInferenceProfiles (Converse-usable ids); " +
		"ListFoundationModels / OpenAI-compat shapes are not reliable Converse keep-sets.",

	providerprofile.DiscoveryVertex: "Model Garden publishers/google/models with " +
		"supportedActions.openGenerationAiStudio; OpenAI-compat /models is not the Garden catalog.",
}

// DiscoveryKeepSetGap returns the documented capability gap for strategy, if any.
func DiscoveryKeepSetGap(strategy providerprofile.DiscoveryStrategy) (string, bool) {
	gap, ok := discoveryKeepSet[strategy]
	return gap, ok
}

// DiscoveryKeepSetSnapshot returns a copy of the mint-rule table.
func DiscoveryKeepSetSnapshot() map[providerprofile.DiscoveryStrategy]string {
	out := make(map[providerprofile.DiscoveryStrategy]string, len(discoveryKeepSet))
	for k, v := range discoveryKeepSet {
		out[k] = v
	}
	return out
}
