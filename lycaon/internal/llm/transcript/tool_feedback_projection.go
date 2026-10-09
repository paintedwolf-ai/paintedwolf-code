package transcript

import (
	"encoding/json"

	"github.com/lycaon/lycaon/pkg/api"
)

const maxToolFeedbackBytes = 32 << 10

// Replacement calls and preconditions travel as structured host data.
func toolFeedbackParts(msg api.Message) []api.MessageContentPart {
	if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
		return nil
	}
	type entry struct {
		api.ToolFeedback
		DetailsOmitted  bool   `json:"details_omitted,omitempty"`
		ResultMessageID string `json:"result_message_id,omitempty"`
	}
	var entries []json.RawMessage
	budget := maxToolFeedbackBytes - 128
	omitted := 0
	for _, feedback := range msg.ToolResult.Feedback {
		if len(feedback.Details) == 0 && feedback.Subject == nil {
			continue
		}
		raw, err := json.Marshal(entry{ToolFeedback: feedback})
		if err != nil || len(raw) > budget {
			compact := api.ToolFeedback{Code: feedback.Code}
			if msg.ToolResult.Tool == "submit_verdict" {
				compact.Details = boundedVerdictDiagnostics(feedback.Details)
			}
			raw, err = json.Marshal(entry{ToolFeedback: compact, DetailsOmitted: true, ResultMessageID: msg.ID})
			if err != nil || len(raw)+1 > budget {
				raw, err = json.Marshal(entry{ToolFeedback: api.ToolFeedback{Code: feedback.Code}, DetailsOmitted: true, ResultMessageID: msg.ID})
			}
		}
		if err != nil || len(raw)+1 > budget || len(entries) >= 32 {
			omitted++
			continue
		}
		entries = append(entries, raw)
		budget -= len(raw) + 1
	}
	if len(entries) == 0 && omitted == 0 {
		return nil
	}
	body, err := json.Marshal(struct {
		Feedback []json.RawMessage `json:"tool_feedback"`
		Omitted  int               `json:"omitted,omitempty"`
	}{entries, omitted})
	if err != nil {
		return nil
	}
	return []api.MessageContentPart{{
		Content: string(body), Origin: api.MessageOriginHost,
		Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted,
		Source: "tool_feedback",
	}}
}

// The durable result retains the full diagnostic set. Projection keeps whole
// issue records so a large candidate example cannot hide the repairs.
func boundedVerdictDiagnostics(details map[string]any) map[string]any {
	raw, err := json.Marshal(details)
	if err != nil {
		return nil
	}
	var source map[string]any
	if json.Unmarshal(raw, &source) != nil {
		return nil
	}
	out := map[string]any{}
	budget := 12 << 10
	for _, key := range []string{"workflow_phase", "field", "field_path", "issues", "schema_issues", "repairs", "unknown_groups", "unaccounted_group_ids", "reason"} {
		value, ok := source[key]
		if !ok {
			continue
		}
		if rows, ok := value.([]any); ok {
			kept := []any{}
			for _, row := range rows[:min(len(rows), 8)] {
				if repair, ok := row.(map[string]any); ok && key == "repairs" {
					row = map[string]any{"code": repair["code"], "details": boundedVerdictDiagnosticsMap(repair["details"])}
				}
				encoded, err := json.Marshal(row)
				if err != nil || len(encoded)+1 > budget {
					break
				}
				kept = append(kept, row)
				budget -= len(encoded) + 1
			}
			out[key] = kept
			out[key+"_omitted"] = len(rows) - len(kept)
		} else if encoded, err := json.Marshal(value); err == nil && len(encoded) <= budget {
			out[key] = value
			budget -= len(encoded)
		}
	}
	return out
}

func boundedVerdictDiagnosticsMap(value any) map[string]any {
	details, _ := value.(map[string]any)
	return boundedVerdictDiagnostics(details)
}
