package history

import (
	"encoding/json"

	"github.com/lycaon/lycaon/internal/hostmarker"
)

// projectRecallSearch retains source addresses before dropping observation bodies.
func projectRecallSearch(raw string, maxBytes int) (string, bool) {
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(raw)
	if !ok {
		return "", false
	}
	var root map[string]any
	if json.Unmarshal([]byte(body), &root) != nil {
		return "", false
	}
	hits, ok := root["hits"].([]any)
	if !ok || len(hits) == 0 {
		return "", false
	}
	originalHits := len(hits)
	withRecord := make([]any, 0, len(hits))
	withAddress := make([]any, 0, len(hits))
	for _, rawHit := range hits {
		hit, ok := rawHit.(map[string]any)
		if !ok {
			return "", false
		}
		handle, _ := hit["handle"].(string)
		record, _ := hit["record_request"].(map[string]any)
		messageID, _ := record["message_id"].(string)
		delete(hit, "snippet")
		if _, exists := hit["body"]; exists {
			delete(hit, "body")
			hit["body_truncated"] = true
		}
		if messageID != "" {
			withRecord = append(withRecord, hit)
		}
		if handle != "" || messageID != "" {
			withAddress = append(withAddress, hit)
		}
	}
	switch {
	case len(withRecord) > 0:
		hits = withRecord
	case len(withAddress) > 0:
		hits = withAddress
	default:
		return "", false
	}
	if _, spilled := root["wire_spill_path"]; spilled {
		delete(root, "wire_spill_path")
		suffix = ""
	}
	total := max(int(jsonInt(root["hits_total"])), originalHits)
	previouslyTruncated, _ := root["hits_truncated"].(bool)
	root["truncated"] = true
	root["wire_compacted"] = true
	for count := len(hits); count >= 1; count-- {
		shown := hits[:count]
		if count > 1 && count < len(hits) {
			shown = append(append([]any{}, hits[:count-1]...), hits[len(hits)-1])
		}
		root["hits"] = shown
		root["hits_shown"] = len(shown)
		root["hits_total"] = total
		root["hits_truncated"] = previouslyTruncated || len(hits) < originalHits || count < len(hits)
		if receipt, ok := root["receipt"].(map[string]any); ok {
			receipt["truncated"] = true
		}
		encoded, err := json.Marshal(root)
		if err != nil {
			return "", false
		}
		if len(prefix)+len(encoded)+len(suffix) <= maxBytes {
			return prefix + string(encoded) + suffix, true
		}
	}
	return "", false
}
