package llm

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"sort"
	"strings"
	"time"
)

// CatalogAuthoritative reports whether assignable models should use the
// catalog-first merge (mapped kind ∧ usable modelfeed document).
func CatalogAuthoritative(kind string, feedUsable bool) bool {
	switch kind {
	case "azure", "vertex", "vertex-express":
		// Feed rows provide metadata and pricing for these transports, but their
		// assignable ids must come from deployments or typed live discovery.
		return false
	}
	_, mapped := modelfeed.FeedKeyForKind(kind)
	return mapped && feedUsable
}

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

const discoverModelsTimeout = 2 * time.Second

func catalogPricingForID(kind string, catalog []modelinfo.Entry, id string) (modelinfo.Entry, bool) {
	for _, m := range catalog {
		if m.ID == id {
			return m, true
		}
	}
	for _, m := range catalog {
		if modelinfo.EquivalentID(kind, m.ID, id) {
			entry := m
			entry.ID = id
			return entry, true
		}
	}
	return modelinfo.Entry{}, false
}

func mergeDiscoveredModelEntry(kind string, catalog []modelinfo.Entry, discovered modelinfo.Entry) modelinfo.Entry {
	if priced, ok := catalogPricingForID(kind, catalog, discovered.ID); ok {
		return enrichFromDiscovery(priced, discovered)
	}
	return discovered
}

// applyDiscoveredModels builds the discovery-authoritative visible list from live
// rows. Static local models[] supply control overlays for matching ids.
// Empty discovery yields an empty list (catalog-authoritative merge is handled
// by mergeAssignableModels). Role exclusions filter pickers/readiness separately.
func applyDiscoveredModels(kind string, catalog []modelinfo.Entry, discovered []modelinfo.Entry) []modelinfo.Entry {
	if len(discovered) == 0 {
		return nil
	}
	out := make([]modelinfo.Entry, 0, len(discovered))
	for _, item := range discovered {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		out = append(out, mergeDiscoveredModelEntry(kind, catalog, item))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// Lite JSON Schemas for utility Completes. These constrain
// freeform content without tool calling. Host parse remains resilient when a
// provider ignores or rejects response_format.

var (
	curateSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["selections", "gloss"],
  "properties": {
    "selections": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["path", "line", "excerpt"],
        "properties": {
          "path": { "type": "string" },
          "line": { "type": "integer" },
          "excerpt": { "type": "string" }
        }
      }
    },
    "gloss": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["label"],
        "properties": {
          "label": { "type": "string" }
        }
      }
    }
  }
}`)
)

// CurateResponseFormat constrains host synthesis-curate selection JSON.
func CurateResponseFormat() *modelcall.ResponseFormat {
	return modelcall.JSONSchemaFormat("curate", curateSchema)
}
