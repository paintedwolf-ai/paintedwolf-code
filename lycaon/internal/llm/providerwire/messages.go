package providerwire

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// SystemPreambleEnd is the exclusive end of the standing system content.
// An assembly boundary closes it even when history starts with a system row.
// Utility requests without a boundary use their leading system messages.
func SystemPreambleEnd(messages []api.Message) int {
	for i, message := range messages {
		if message.Role != api.MessageRoleSystem || HostConversationEvent(message) {
			return i
		}
		if message.PromptCacheBreakpoint == api.PromptCacheTierStanding {
			return i + 1
		}
	}
	return len(messages)
}

// SystemPreamble keeps host events in order for templates with one system slot.
func SystemPreamble(messages []api.Message) []api.Message {
	out := make([]api.Message, 0, len(messages))
	leading := SystemPreambleEnd(messages)
	var parts []string
	for _, message := range messages[:leading] {
		parts = append(parts, message.Content)
	}
	if leading > 0 {
		preamble := messages[0]
		preamble.Content = strings.Join(parts, "\n\n")
		// The merged block ends at the last boundary inside it.
		for _, message := range messages[:leading] {
			if message.PromptCacheBreakpoint != api.PromptCacheTierNone {
				preamble.PromptCacheBreakpoint = message.PromptCacheBreakpoint
			}
		}
		out = append(out, preamble)
	}
	for _, message := range messages[leading:] {
		if message.Role == api.MessageRoleSystem {
			message.Role = api.MessageRoleUser
		}
		out = append(out, message)
	}
	return out
}

// Host events start conversation content even before the first user message.
func HostConversationEvent(message api.Message) bool {
	return message.Origin == api.MessageOriginHost && message.Visibility == api.MessageVisibilityInternal && message.Kind != ""
}

// ReasoningForModel returns traces to their originating provider and model only.
func ReasoningForModel(m api.Message, providerID, model string) *api.ModelReasoning {
	r := m.ModelReasoning
	if r == nil || r.Text == "" && len(r.Details) == 0 {
		return nil
	}
	if providerID == "" || model == "" || r.ProviderID != providerID || r.Model != model {
		return nil
	}
	return r
}
