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
		DetailsOmitted bool `json:"details_omitted,omitempty"`
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
			raw, err = json.Marshal(entry{ToolFeedback: api.ToolFeedback{Code: feedback.Code}, DetailsOmitted: true})
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
