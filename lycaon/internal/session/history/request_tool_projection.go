package history

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

// projectRecordedToolBodies preserves active exchanges while moving large bodies behind recall.
func projectRecordedToolBodies(ctx context.Context, rows []api.Message, tokens ...int) []api.Message {
	out := append([]api.Message(nil), rows...)
	for i, row := range out {
		if !isOversizedToolBody(row) {
			continue
		}
		if len(tokens) > 0 && tokens[0] > 1 && compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(out)) <= tokens[0] {
			break
		}
		raw := row.Content
		if row.ModelProjected {
			raw = row.ToolResult.Content
		}
		content, fitted, isHistory := raw, len(raw) <= 2048, false
		if !fitted {
			content, fitted, isHistory = projectRecallHistoryPage(ctx, raw, 2048, row.ToolResult.ToolArgs)
			if !fitted && !isHistory && row.ToolResult.Tool == "recall" {
				content, fitted = projectRecallSearch(raw, 2048)
			}
		}
		if !fitted {
			record := recall.HistoryRequest{ThroughOrd: row.Ord, MessageID: row.ID,
				SourceSeq: row.Seq, Format: "text", BodyRunes: 4096}
			envelope, err := json.Marshal(struct {
				Compacted bool                  `json:"compacted"`
				Outcome   api.ToolResultOutcome `json:"outcome,omitempty"`
				Record    recall.HistoryRequest `json:"record_request"`
			}{true, row.ToolResult.Outcome, record})
			if err != nil {
				continue
			}
			content = string(envelope)
			historyJSON, err := json.Marshal(record)
			if err == nil {
				if rendered, err := guidance.RenderGuidance(ctx, "compaction/tool-result-reference", map[string]any{
					"tool": row.ToolResult.Tool, "outcome": row.ToolResult.Outcome,
					"evidence_handles": row.EvidenceHandles, "history_json": string(historyJSON),
				}); err == nil && rendered != "" && len(rendered) <= 2048 {
					content = rendered
				}
			}
			if !isHistory {
				if partial, fitted := tooloutput.FitWireJSON(raw, 2048-len(content)-1); fitted {
					content += "\n" + partial
				}
			}
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
		out[i] = row
	}
	return out
}

func projectRecallHistoryPage(ctx context.Context, raw string, maxBytes int, originalArgs map[string]any) (string, bool, bool) {
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(raw)
	if !ok {
		return "", false, false
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
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
	if action, err := guidance.RenderGuidance(ctx, "recall/history-continuation", nil); err == nil {
		root["next_action"] = action
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
				if coordinates := recallContinuationCoordinates(root, originalArgs); coordinates != nil {
					coordinates["offset"] = next
					if _, present := coordinates["source_seq"]; !present {
						if seq, present := entry["seq"]; present {
							coordinates["source_seq"] = seq
						}
					}
				}
				root["truncated"] = true
			}
			if receipt, ok := root["receipt"].(map[string]any); ok {
				receipt["bytes_returned"] = len(trimmed)
				receipt["truncated"] = true
			}
			if encoded, err := json.Marshal(root); err == nil && len(prefix)+len(encoded)+len(suffix) <= maxBytes {
				best, low = prefix+string(encoded)+suffix, mid+1
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
				if coordinates := recallContinuationCoordinates(root, originalArgs); coordinates != nil {
					coordinates["after_ord"] = last["ord"]
				}
			}
			root["truncated"] = true
		}
		if encoded, err := json.Marshal(root); err == nil && len(prefix)+len(encoded)+len(suffix) <= maxBytes {
			best, low = prefix+string(encoded)+suffix, mid+1
		} else {
			high = mid - 1
		}
	}
	return best, best != "", true
}

func recallContinuationCoordinates(root, originalArgs map[string]any) map[string]any {
	request, ok := root["next_request"].(map[string]any)
	if !ok {
		coordinates, ok := originalArgs["history"].(map[string]any)
		if !ok {
			return nil
		}
		request = map[string]any{"history": maps.Clone(coordinates)}
		if limit, ok := originalArgs["limit"]; ok {
			request["limit"] = limit
		}
		root["next_request"] = request
	}
	coordinates, _ := request["history"].(map[string]any)
	return coordinates
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
	return true
}
