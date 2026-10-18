package transcript

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Tool-step text is retained as a private note, matching transcript visibility.
func frameInternalStepNote(msg api.Message) api.Message {
	if msg.Role != api.MessageRoleAssistant || len(msg.ToolCalls) == 0 || api.IsAgentNoteMessage(msg) || strings.TrimSpace(msg.Content) == "" {
		return msg
	}
	loadContentAuthority()
	parts := []api.MessageContentPart{{
		Content: internalStepNoteNotice, Origin: api.MessageOriginHost,
		Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted,
		Source: "internal_step_note",
	}}
	msg.ContentParts = append(parts, api.MessageTextParts(msg)...)
	msg.Content = ProjectContentParts(msg.Role, msg.ContentParts)
	return msg
}
