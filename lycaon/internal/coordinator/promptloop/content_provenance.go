package promptloop

import (
	"context"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/internal/guidance"
)

func oarContentSegments(messages []api.Message) []oar.ContentSegment {
	var out []oar.ContentSegment
	for _, raw := range messages {
		msg := api.NormalizeMessageProvenance(raw)
		for _, part := range api.MessageTextParts(msg) {
			if part.Content == "" {
				continue
			}
			out = append(out, oarContentSegment(
				part.Content, msg.Role, part.Origin, part.Authority, part.TrustTier, part.Source,
			))
		}
	}
	return out
}

func oarModelOutputSegments(content string) []oar.ContentSegment {
	return []oar.ContentSegment{
		oarContentSegment(content, api.MessageRoleAssistant, api.MessageOriginModel, api.ContentAuthorityNone, api.ContentTrustTierTrusted, "model_output"),
	}
}

func oarContentSegment(
	content string,
	role api.MessageRole,
	origin api.MessageOrigin,
	authority api.ContentAuthority,
	trustTier api.ContentTrustTier,
	source string,
) oar.ContentSegment {
	return oar.ContentSegment{
		Content:   content,
		Role:      oarContentRole(role, authority),
		Origin:    oarContentOrigin(origin),
		Authority: oarContentAuthority(authority),
		TrustTier: oarContentTrustTier(trustTier),
		Source:    source,
	}
}

func oarContentRole(role api.MessageRole, authority api.ContentAuthority) string {
	if authority == api.ContentAuthorityDeveloper {
		return "developer"
	}
	switch role {
	case api.MessageRoleSystem:
		return "system"
	case api.MessageRoleUser:
		return "user"
	case api.MessageRoleAssistant:
		return "assistant"
	case api.MessageRoleTool:
		return "tool"
	default:
		return "unknown"
	}
}

func oarContentOrigin(origin api.MessageOrigin) string {
	switch origin {
	case api.MessageOriginHost:
		return "host"
	case api.MessageOriginUser:
		return "user"
	case api.MessageOriginModel:
		return "model"
	case api.MessageOriginTool:
		return "tool"
	case api.MessageOriginPeerAgent:
		return "peer"
	case api.MessageOriginAttachment, api.MessageOriginProject:
		return "resource"
	case api.MessageOriginRetrieval:
		return "retrieval"
	default:
		return "unknown"
	}
}

func oarContentAuthority(authority api.ContentAuthority) string {
	switch authority {
	case api.ContentAuthoritySystem:
		return "system"
	case api.ContentAuthorityDeveloper:
		return "developer"
	case api.ContentAuthorityUser:
		return "user"
	case api.ContentAuthorityUnknown, "":
		return "unknown"
	default:
		return "none"
	}
}

func oarContentTrustTier(trustTier api.ContentTrustTier) string {
	switch trustTier {
	case api.ContentTrustTierUntrusted:
		return "untrusted"
	case api.ContentTrustTierUnknown, "":
		return "unknown"
	default:
		return "trusted"
	}
}

func (l modelTurn) overlayRootPaths(ctx context.Context, sess *api.Session) []string {
	if l.PromptLoop == nil || l.Deps.OverlayRootPaths == nil || sess == nil {
		return nil
	}
	return l.Deps.OverlayRootPaths(ctx, sess)
}

func (l modelTurn) evaluateContentAnchor(ctx context.Context, sess *api.Session, anchor string, segments []oar.ContentSegment, tool string, args map[string]any) (*guidance.Refusal, bool, string, bool) {
	if l.PromptLoop == nil || l.Deps.EvaluateContentAnchor == nil {
		return nil, false, "", false
	}
	return l.Deps.EvaluateContentAnchor(ctx, sess, anchor, segments, tool, args)
}
