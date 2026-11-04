package api

import (
	"strings"

	"github.com/google/uuid"
)

// MidTurnNoteID is the note row derived from an assistant message that carries
// tool calls. The primary key makes publication idempotent per carrier, and the
// derivation is the durable carrier-to-note relationship.
func MidTurnNoteID(carrierID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("agent-note:"+strings.TrimSpace(carrierID))).String()
}

// dropDerivedMidTurnNotes removes note rows whose carrier is present: the carrier
// already holds that text in native position. A note whose carrier was compacted
// away stays, so the model still reads it once.
func dropDerivedMidTurnNotes(messages []Message) []Message {
	derived := make(map[string]struct{})
	for _, msg := range messages {
		if msg.Role == MessageRoleAssistant && len(msg.ToolCalls) > 0 && strings.TrimSpace(msg.ID) != "" {
			derived[MidTurnNoteID(msg.ID)] = struct{}{}
		}
	}
	if len(derived) == 0 {
		return messages
	}
	out := messages[:0:0]
	for _, msg := range messages {
		if _, ok := derived[msg.ID]; ok && IsAgentNoteMessage(msg) {
			continue
		}
		out = append(out, msg)
	}
	return out
}
