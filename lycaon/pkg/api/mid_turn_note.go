package api

import (
	"crypto/sha1"
	"fmt"
	"strings"

)

// uuidNamespaceOID is the RFC 9562 OID namespace. Deriving here keeps this wire package
// free of the uuid module, which links net and so cgo into anything importing it.
var uuidNamespaceOID = [16]byte{0x6b, 0xa7, 0xb8, 0x12, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}

// MidTurnNoteID is the note row derived from an assistant message that carries
// tool calls. The primary key makes publication idempotent per carrier, and the
// derivation is the durable carrier-to-note relationship.
func MidTurnNoteID(carrierID string) string {
	hash := sha1.New() //nolint:gosec // RFC 9562 version 5 names an identity; it protects nothing.
	hash.Write(uuidNamespaceOID[:])
	hash.Write([]byte("agent-note:" + strings.TrimSpace(carrierID)))
	id := hash.Sum(nil)[:16]
	id[6], id[8] = id[6]&0x0f|0x50, id[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
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
