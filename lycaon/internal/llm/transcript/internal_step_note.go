package transcript

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const legacyInternalStepNoteNotice = "Internal work note from a tool step; not shown to the user. Still send the final user-visible reply."

// Tool-step text is retained as a private note, matching transcript visibility.
func frameInternalStepNote(msg api.Message) api.Message {
	if msg.Role != api.MessageRoleAssistant || api.IsAgentNoteMessage(msg) || strings.TrimSpace(msg.Content) == "" {
		return msg
	}
	loadContentAuthority()
	if len(msg.ToolCalls) == 0 {
		if len(msg.ContentParts) == 0 {
			msg.Content = stripParrotedInternalStepNote(msg.Content)
		}
		return msg
	}
	var textParts []api.MessageContentPart
	for _, part := range api.MessageTextParts(msg) {
		if part.Origin == api.MessageOriginHost && part.Source == internalStepNoteSource {
			continue
		}
		cleaned := stripParrotedInternalStepNote(part.Content)
		if cleaned == "" {
			continue
		}
		part.Content = cleaned
		part.Origin = api.MessageOriginModel
		part.Authority = api.ContentAuthorityNone
		part.TrustTier = api.ContentTrustTierUntrusted
		part.Source = internalStepNoteSource
		textParts = append(textParts, part)
	}
	if len(textParts) == 0 {
		msg.Content = ""
		msg.ContentParts = nil
		return msg
	}
	msg.ContentParts = append([]api.MessageContentPart{{
		Content: internalStepNoteNotice, Origin: api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		Source: internalStepNoteSource,
	}}, textParts...)
	msg.Content = ProjectContentParts(msg.Role, msg.ContentParts)
	return msg
}

func stripParrotedInternalStepNote(content string) string {
	s := strings.ReplaceAll(content, "\r\n", "\n")
	stripped := false
	for {
		trimmed := strings.TrimLeft(s, " \t\n")
		if trimmed == "" {
			return ""
		}
		line, rest, _ := strings.Cut(trimmed, "\n")
		if isParrotedInternalStepNoteLine(line) {
			s = rest
			stripped = true
			continue
		}
		break
	}
	s = strings.TrimSpace(s)
	if !stripped || s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		for strings.HasPrefix(line, "⟦D⟧") {
			line = strings.TrimPrefix(line, "⟦D⟧")
		}
		lines[i] = line
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func isParrotedInternalStepNoteLine(line string) bool {
	plain := strings.TrimSpace(line)
	for strings.HasPrefix(plain, "⟦D⟧") {
		plain = strings.TrimSpace(strings.TrimPrefix(plain, "⟦D⟧"))
	}
	switch plain {
	case "⟦D:host:internal_step_note⟧",
		"⟦D:host:internal_step_note⟦",
		"⟦D:model:internal_step_note⟧",
		"⟦D:model:internal_step_note⟦",
		internalStepNoteNotice,
		legacyInternalStepNoteNotice:
		return true
	default:
		return false
	}
}
