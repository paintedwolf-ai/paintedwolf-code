package transcript

import (
	"encoding/json"

	"github.com/lycaon/lycaon/pkg/api"
)

const maxToolFeedbackBytes = 32 << 10

// Invocation outcomes and remedies travel as structured host data.
func toolFeedbackParts(msg api.Message) []api.MessageContentPart {
	if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
		return nil
	}
	type entry struct {
		api.ToolFeedback
		DetailsOmitted  bool   `json:"details_omitted,omitempty"`
		ResultMessageID string `json:"result_message_id,omitempty"`
	}
	invocation, invocationOmitted := invocationFeedback(msg.ToolResult)
	var entries []json.RawMessage
	budget := maxToolFeedbackBytes - toolFeedbackEnvelopeReserve - len(invocation)
	omitted := 0
	for _, feedback := range msg.ToolResult.Feedback {
		if len(feedback.Details) == 0 && feedback.Subject == nil {
			continue
		}
		raw, err := json.Marshal(entry{ToolFeedback: feedback})
		if err != nil || len(raw) > budget {
			compact := api.ToolFeedback{Code: feedback.Code}
			limit := maxHostDiagnosticBytes
			if msg.ToolResult.Tool == "submit_verdict" {
				limit = maxVerdictDiagnosticBytes
			}
			compact.Details = boundedHostDiagnostics(feedback.Details, limit)
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
	if len(entries) == 0 && omitted == 0 && len(invocation) == 0 && !invocationOmitted {
		return nil
	}
	resultReference := ""
	if invocationOmitted {
		resultReference = msg.ID
	}
	body, err := json.Marshal(struct {
		Feedback          []json.RawMessage `json:"tool_feedback,omitempty"`
		Omitted           int               `json:"omitted,omitempty"`
		Invocation        json.RawMessage   `json:"invocation,omitempty"`
		InvocationOmitted bool              `json:"invocation_omitted,omitempty"`
		ResultMessageID   string            `json:"result_message_id,omitempty"`
	}{entries, omitted, invocation, invocationOmitted, resultReference})
	if err != nil {
		return nil
	}
	return []api.MessageContentPart{{
		Content: string(body), Origin: api.MessageOriginHost,
		Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted,
		Source: "tool_feedback",
	}}
}

func invocationFeedback(result *api.ToolResult) (json.RawMessage, bool) {
	receipt := result.Invocation
	if receipt == nil || receipt.ID == "" || receipt.Tool != result.Tool || receipt.ToolCallID != result.ToolCallID {
		return nil, false
	}
	code := ""
	if receipt.Failure != nil {
		code = receipt.Failure.Code
	}
	body, err := json.Marshal(struct {
		ID               string               `json:"id"`
		Tool             string               `json:"tool"`
		ToolCallID       string               `json:"tool_call_id"`
		Status           api.InvocationStatus `json:"status"`
		Invoked          bool                 `json:"invoked"`
		Code             string               `json:"code,omitempty"`
		SourceRevision   string               `json:"source_revision,omitempty"`
		SourceRootDigest string               `json:"source_root_digest,omitempty"`
		SourceVerdict    string               `json:"source_verdict,omitempty"`
	}{receipt.ID, receipt.Tool, receipt.ToolCallID, receipt.Status, receipt.Invoked, code, receipt.SourceRevision, receipt.SourceRootDigest, receipt.SourceVerdict})
	if err != nil || len(body) > maxInvocationFeedbackBytes {
		return nil, true
	}
	return body, false
}
