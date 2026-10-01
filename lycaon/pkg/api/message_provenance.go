package api

import "strings"

// NormalizeMessageProvenance fills absent provenance from structured message
// state. It never inspects prose. Callers with mixed or elevated content stamp
// explicit values or ContentParts before this function runs.
func NormalizeMessageProvenance(msg Message) Message {
	if msg.Origin == "" || msg.Authority == "" || msg.TrustTier == "" {
		origin, authority, trustTier := defaultMessageProvenance(msg)
		if msg.Origin == "" {
			msg.Origin = origin
		}
		if msg.Authority == "" {
			msg.Authority = authority
		}
		if msg.TrustTier == "" {
			msg.TrustTier = trustTier
		}
	}
	if len(msg.ContentParts) > 0 {
		parts := make([]MessageContentPart, len(msg.ContentParts))
		copy(parts, msg.ContentParts)
		msg.ContentParts = parts
	}
	for i := range msg.ContentParts {
		part := &msg.ContentParts[i]
		if part.Origin == "" {
			part.Origin = msg.Origin
		}
		if part.Authority == "" {
			part.Authority = ContentAuthorityNone
			if part.Origin == msg.Origin {
				part.Authority = msg.Authority
			}
		}
		if part.TrustTier == "" {
			part.TrustTier = defaultContentTrustTier(part.Origin)
			if part.Origin == msg.Origin {
				part.TrustTier = msg.TrustTier
			}
		}
		part.Source = strings.TrimSpace(part.Source)
		part.MediaType = strings.TrimSpace(part.MediaType)
	}
	return msg
}

func defaultContentTrustTier(origin MessageOrigin) ContentTrustTier {
	switch origin {
	case MessageOriginHost, MessageOriginUser, MessageOriginModel, MessageOriginProject:
		return ContentTrustTierTrusted
	case MessageOriginTool, MessageOriginPeerAgent, MessageOriginAttachment, MessageOriginRetrieval:
		return ContentTrustTierUntrusted
	default:
		return ContentTrustTierUnknown
	}
}

func defaultMessageProvenance(msg Message) (MessageOrigin, ContentAuthority, ContentTrustTier) {
	switch msg.Role {
	case MessageRoleSystem:
		return MessageOriginHost, ContentAuthoritySystem, ContentTrustTierTrusted
	case MessageRoleAssistant:
		return MessageOriginModel, ContentAuthorityNone, ContentTrustTierTrusted
	case MessageRoleTool:
		return MessageOriginTool, ContentAuthorityNone, ContentTrustTierUntrusted
	case MessageRoleUser:
		if msg.Visibility == MessageVisibilityInternal {
			return MessageOriginHost, ContentAuthoritySystem, ContentTrustTierTrusted
		}
		return MessageOriginUser, ContentAuthorityUser, ContentTrustTierTrusted
	default:
		return MessageOriginUnknown, ContentAuthorityUnknown, ContentTrustTierUnknown
	}
}

// MessageTextParts returns explicit mixed-origin parts, or one synthesized part
// for ordinary messages. The returned slice never aliases ContentParts.
func MessageTextParts(msg Message) []MessageContentPart {
	msg = NormalizeMessageProvenance(msg)
	if len(msg.ContentParts) == 0 {
		return []MessageContentPart{{
			Content: msg.Content, Origin: msg.Origin, Authority: msg.Authority, TrustTier: msg.TrustTier,
		}}
	}
	out := make([]MessageContentPart, len(msg.ContentParts))
	copy(out, msg.ContentParts)
	return out
}

// MessageUserInstructionContent returns only user-authoritative text while
// preserving its original bytes. External parts in the same visible message
// cannot become workflow feedback, a progress goal, or a compacted task.
func MessageUserInstructionContent(msg Message) string {
	msg = NormalizeMessageProvenance(msg)
	if len(msg.ContentParts) == 0 {
		if msg.Origin == MessageOriginUser && msg.Authority == ContentAuthorityUser {
			return msg.Content
		}
		return ""
	}
	var content []string
	for _, part := range msg.ContentParts {
		if part.Origin == MessageOriginUser && part.Authority == ContentAuthorityUser {
			content = append(content, part.Content)
		}
	}
	return strings.Join(content, "\n\n")
}

// ExternallyAuthored selects provenance marking independently of instruction trust.
func ExternallyAuthored(origin MessageOrigin) bool {
	switch origin {
	case MessageOriginRetrieval, MessageOriginPeerAgent, MessageOriginAttachment:
		return true
	default:
		return false
	}
}

// trustRank orders tiers so a floor can be composed. Unknown sits between: not
// established, which is neither safe nor known-hostile.
func trustRank(tier ContentTrustTier) int {
	switch tier {
	case ContentTrustTierUntrusted:
		return 0
	case ContentTrustTierTrusted:
		return 2
	default:
		return 1
	}
}

// FloorTrustTier returns the least-trusted tier among its inputs, so trust
// cannot increase under transformation: a summary of untrusted content stays
// untrusted rather than becoming model-authored and trusted.
//
// No inputs means nothing was consumed, which is unknown rather than trusted.
func FloorTrustTier(tiers ...ContentTrustTier) ContentTrustTier {
	if len(tiers) == 0 {
		return ContentTrustTierUnknown
	}
	floor := ContentTrustTierTrusted
	for _, tier := range tiers {
		if trustRank(tier) < trustRank(floor) {
			floor = tier
		}
	}
	return floor
}
