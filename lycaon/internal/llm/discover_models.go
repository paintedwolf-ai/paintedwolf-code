package llm

import (
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

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
