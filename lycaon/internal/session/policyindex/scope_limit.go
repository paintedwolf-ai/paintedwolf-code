package policyindex

import (
	"context"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) scopeLimit(ctx context.Context, sess *api.Session, limit scopeLimitError) (api.Message, error) {
	instructions, err := anchor.RenderInform(ctx, anchor.InjectAgentsMD, anchor.MatchContext{Surface: "coordinator", SessionID: sess.ID}, prompts.NewInjectRenderer(m.renderer), map[string]any{"mode": "scope_limit"})
	if err != nil {
		return api.Message{}, err
	}
	facts, err := json.Marshal(limit)
	if err != nil {
		return api.Message{}, err
	}
	parts := []api.MessageContentPart{
		{Content: instructions, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, Source: "agent_policy_scope_limit"},
		{Content: string(facts), Origin: api.MessageOriginHost, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted, Source: "agent_policy_scope_limit_facts"},
	}
	return api.Message{Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, Content: instructions + "\n\n" + string(facts), ContentParts: parts}, nil
}
