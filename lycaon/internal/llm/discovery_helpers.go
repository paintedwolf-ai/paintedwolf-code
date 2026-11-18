package llm

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/modelfeed"
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
