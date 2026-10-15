package history

import (
	"encoding/json"

	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

// projectRecordedToolBodies preserves active exchanges while moving large bodies behind recall.
func projectRecordedToolBodies(rows []api.Message) []api.Message {
	for i, row := range rows {
		if !isOversizedToolBody(row) {
			continue
		}
		content, fitted, isHistory := projectRecallHistoryPage(row.Content, 2048)
		if !fitted && !isHistory {
			content, fitted = tooloutput.FitWireJSON(row.Content, 2048)
		}
		if !fitted {
			raw, err := json.Marshal(struct {
				Compacted bool                  `json:"compacted"`
				Outcome   api.ToolResultOutcome `json:"outcome,omitempty"`
				Record    recall.HistoryRequest `json:"record_request"`
			}{true, row.ToolResult.Outcome, recall.HistoryRequest{ThroughOrd: row.Ord, MessageID: row.ID,
				SourceSeq: row.Seq, Format: "record", BodyRunes: 2048}})
			if err != nil {
				continue
			}
			content = string(raw)
		}
		tiers := []api.ContentTrustTier{row.TrustTier}
		for _, part := range api.MessageTextParts(row) {
			tiers = append(tiers, part.TrustTier)
		}
		row.TrustTier = api.FloorTrustTier(tiers...)
		row.Authority = api.ContentAuthorityNone
		row.Content = content
		row.ContentParts = nil
		row.ModelProjected = false
		result := *row.ToolResult
		result.Content = content
		row.ToolResult = &result
		rows[i] = row
	}
	return rows
}

func projectRecallHistoryPage(raw string, maxBytes int) (string, bool, bool) {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return "", false, false
	}
	history, ok := root["history"].(map[string]any)
	if !ok {
		return "", false, false
	}
	entries, ok := history["entries"].([]any)
	if !ok || len(entries) == 0 {
		return "", false, true
	}
	if format, _ := history["format"].(string); len(entries) == 1 && format != "index" {
		entry, ok := entries[0].(map[string]any)
		if !ok {
			return "", false, true
		}
		body, _ := entry["content"].(string)
		runes := []rune(body)
		if len(runes) == 0 {
			return "", false, true
		}
		offset, totalRunes, prevNext := int(jsonInt(entry["offset"])), int(jsonInt(entry["runes"])), int(jsonInt(history["next_offset"]))
		low, high, best := 1, len(runes), ""
		for low <= high {
			mid := (low + high) / 2
			trimmed := string(runes[:mid])
			entry["content"] = trimmed
			if next := offset + mid; mid < len(runes) || totalRunes > next || prevNext > next {
				history["next_offset"] = next
				root["truncated"] = true
			}
			if receipt, ok := root["receipt"].(map[string]any); ok {
				receipt["bytes_returned"] = len(trimmed)
				receipt["truncated"] = true
			}
			if encoded, err := json.Marshal(root); err == nil && len(encoded) <= maxBytes {
				best, low = string(encoded), mid+1
			} else {
				high = mid - 1
			}
		}
		return best, best != "", true
	}
	low, high, best := 1, len(entries), ""
	for low <= high {
		mid := (low + high) / 2
		history["entries"] = entries[:mid]
		if mid < len(entries) {
			if last, ok := entries[mid-1].(map[string]any); ok {
				history["next_after_ord"] = last["ord"]
			}
			root["truncated"] = true
		}
		if encoded, err := json.Marshal(root); err == nil && len(encoded) <= maxBytes {
			best, low = string(encoded), mid+1
		} else {
			high = mid - 1
		}
	}
	return best, best != "", true
}

func jsonInt(v any) int64 {
	if n, ok := v.(float64); ok {
		return int64(n)
	}
	return 0
}

func hasOversizedToolBody(rows []api.Message) bool {
	for _, row := range rows {
		if isOversizedToolBody(row) {
			return true
		}
	}
	return false
}

func isOversizedToolBody(row api.Message) bool {
	if row.Role != api.MessageRoleTool || row.ToolResult == nil || row.ID == "" || row.Ord <= 0 || len(row.Content) <= 2048 {
		return false
	}
	if row.ToolResult.Tool == recall.ToolName && len(row.Content) <= 16384 {
		return false
	}
	return true
}
