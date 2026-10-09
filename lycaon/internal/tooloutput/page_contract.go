package tooloutput

import (
	"encoding/json"
	"maps"

	"github.com/lycaon/lycaon/internal/tools/surveyjson"
)

// PageContract declares a contiguous, offset-addressed host result collection.
// Compaction may shorten its prefix but cannot skip entries within it.
type PageContract struct {
	Collection string `json:"collection"`
	Revision   string `json:"revision"`
}

func fitPageJSON(original map[string]any, prefix, suffix string, maxBytes int) (string, bool, bool) {
	raw, declared := original["page_contract"]
	if !declared {
		return "", false, false
	}
	contract, ok := raw.(map[string]any)
	if !ok {
		return "", false, true
	}
	key, _ := contract["collection"].(string)
	revision, _ := contract["revision"].(string)
	page, ok := original[key].([]any)
	offset, offsetOK := original["offset"].(json.Number)
	start, err := offset.Int64()
	if !ok || !offsetOK || err != nil || start < 0 || revision == "" {
		return "", false, true
	}
	// A singleton keeps its identity even when its descriptive body needs a spill.
	for count := len(page); count >= 1; count-- {
		for _, textBytes := range []int{1024, 256, 64, 0} {
			projected := maps.Clone(original)
			rows := make([]any, count)
			for i := range rows {
				if item, ok := page[i].(map[string]any); ok {
					rows[i] = budgetObject(item, 2, textBytes, 0)
				} else {
					rows[i] = page[i]
				}
			}
			projected[key] = rows
			projected["wire_compacted"] = true
			if count < len(page) {
				projected["next_offset"] = start + int64(count)
				projected["truncated"] = true
			}
			raw, err := surveyjson.Marshal(projected)
			if err != nil {
				return "", false, true
			}
			out := prefix + string(raw) + suffix
			if len(out) <= maxBytes {
				return out, true, true
			}
		}
	}
	return "", false, true
}
