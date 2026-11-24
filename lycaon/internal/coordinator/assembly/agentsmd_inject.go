package assembly

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/pkg/api"
)

// AgentsMDPolicyDelivery carries resolved policy messages and the active root ID.
type AgentsMDPolicyDelivery struct {
	Messages     []api.Message
	ActiveRootID string
}

// agentsMDIndexInject keeps the session index in every standing prompt prefix.
func (e *turnContextAssembler) agentsMDIndexInject(ctx context.Context, sess *api.Session, turn *TurnAssemblyScratch) []api.Message {
	if e == nil || sess == nil || turn == nil {
		return nil
	}
	deps := e.deps
	if deps.AgentsMDIndex == nil {
		return nil
	}
	block, ok := deps.AgentsMDIndex(ctx, sess)
	if !ok || strings.TrimSpace(block.Content) == "" {
		return nil
	}
	block.ContextPinned = true
	return []api.Message{block}
}

// agentsMDChainInject loads current working guidance for resolved scopes.
func (e *turnContextAssembler) agentsMDChainInject(ctx context.Context, sess *api.Session, history []api.Message, turn *TurnAssemblyScratch) (AgentsMDPolicyDelivery, error) {
	if e == nil || sess == nil || turn == nil || e.deps.AgentsMDPolicies == nil {
		return AgentsMDPolicyDelivery{}, nil
	}
	return e.deps.AgentsMDPolicies(ctx, sess, history)
}

// splitPreHistoryAgentsMDPolicies lifts the active root's stable policy blocks
// before history when doing so preserves jurisdiction and nested/overlay order.
func splitPreHistoryAgentsMDPolicies(sess *api.Session, delivery AgentsMDPolicyDelivery) (preHistory, tail []api.Message) {
	if len(delivery.Messages) == 0 {
		return nil, nil
	}
	activeRootID := resolveActivePolicyRootID(sess, delivery)
	if activeRootID == "" {
		return nil, delivery.Messages
	}
	overlayRel := settingsoverlay.Rel("AGENTS.md")
	hasNestedForActiveRoot := false
	for _, msg := range delivery.Messages {
		rootID, path, ok := projectPolicyLocation(msg)
		if ok && rootID == activeRootID && path != "AGENTS.md" && path != overlayRel {
			hasNestedForActiveRoot = true
			break
		}
	}
	for _, msg := range delivery.Messages {
		rootID, path, ok := projectPolicyLocation(msg)
		if ok && rootID == activeRootID && (path == "AGENTS.md" || (path == overlayRel && !hasNestedForActiveRoot)) {
			preHistory = append(preHistory, msg)
			continue
		}
		tail = append(tail, msg)
	}
	return preHistory, tail
}

func resolveActivePolicyRootID(sess *api.Session, delivery AgentsMDPolicyDelivery) string {
	if id := strings.TrimSpace(delivery.ActiveRootID); id != "" {
		return id
	}
	if sess != nil {
		if id := strings.TrimSpace(sess.WorkspaceRootID); id != "" {
			return id
		}
	}
	singleRoot := ""
	for _, msg := range delivery.Messages {
		rootID, _, ok := projectPolicyLocation(msg)
		if !ok {
			continue
		}
		if singleRoot == "" {
			singleRoot = rootID
		} else if singleRoot != rootID {
			return ""
		}
	}
	return singleRoot
}

func projectPolicyLocation(msg api.Message) (string, string, bool) {
	if msg.Role != api.MessageRoleSystem || msg.Origin != api.MessageOriginProject ||
		msg.Authority != api.ContentAuthorityDeveloper || msg.TrustTier != api.ContentTrustTierTrusted ||
		len(msg.ContentParts) != 0 || msg.SourceContext == nil || len(msg.SourceContext.Locations) != 1 {
		return "", "", false
	}
	loc := msg.SourceContext.Locations[0]
	if loc.RootID == "" || loc.Path == "" {
		return "", "", false
	}
	return loc.RootID, loc.Path, true
}
