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
			if len(out) > 0 && out[len(out)-1].Role == api.MessageRoleTool {
				foldSystemIntoTool(&out[len(out)-1], message)
				continue
			}
			message.Role = api.MessageRoleUser
		}
		out = append(out, message)
	}
	return out
}

func foldSystemIntoTool(tool *api.Message, system api.Message) {
	base := tool.Content
	if base == "" && tool.ToolResult != nil {
		base = tool.ToolResult.Content
	}
	tool.Content = joinContent(base, system.Content)
	if tool.ToolResult != nil {
		tr := *tool.ToolResult
		tr.Content = joinContent(tr.Content, system.Content)
		tool.ToolResult = &tr
	}
	if system.PromptCacheBreakpoint != api.PromptCacheTierNone {
		tool.PromptCacheBreakpoint = system.PromptCacheBreakpoint
	}
}

func joinContent(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n\n" + b
	}
}

// Host events start conversation content even before the first user message.
func HostConversationEvent(message api.Message) bool {
	return message.Origin == api.MessageOriginHost && message.Visibility == api.MessageVisibilityInternal && message.Kind != ""
}

// ReasoningReplayScope describes the transport's accepted history.
type ReasoningReplayScope uint8

const (
	ReasoningReplayNone ReasoningReplayScope = iota
	ReasoningReplayHistory
	ReasoningReplayCurrentToolLoop
)

// ProjectReasoning selects replay state without changing durable messages.
func ProjectReasoning(messages []api.Message, providerID, model string, scope ReasoningReplayScope) []api.Message {
	out := append([]api.Message(nil), messages...)
	start := 0
	if scope == ReasoningReplayCurrentToolLoop {
		start = api.UserIntentBoundary(messages)
	}
	for i := range out {
		m := &out[i]
		r := m.ModelReasoning
		eligible := scope != ReasoningReplayNone && i >= start && m.Role == api.MessageRoleAssistant
		if scope == ReasoningReplayCurrentToolLoop && len(m.ToolCalls) == 0 {
			eligible = false
		}
		if !eligible || r == nil || r.Text == "" && len(r.Details) == 0 || providerID == "" || model == "" || r.ProviderID != providerID || r.Model != model {
			m.ModelReasoning = nil
		}
	}
	return out
}
