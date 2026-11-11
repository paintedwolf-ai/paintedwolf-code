package policyindex

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) unavailable(ctx context.Context, sess *api.Session, cause error) (Delivery, error) {
	if err := ctx.Err(); err != nil {
		return Delivery{}, err
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return Delivery{}, cause
	}
	instructions, err := anchor.RenderInform(ctx, anchor.InjectAgentsMD, anchor.MatchContext{Surface: "coordinator", SessionID: sess.ID}, prompts.NewInjectRenderer(m.renderer), map[string]any{"mode": "unavailable"})
	if err != nil {
		return Delivery{}, err
	}
	text := []rune(cause.Error())
	truncated := len(text) > 2048
	diagnostic, err := json.Marshal(struct {
		Status     Status `json:"status"`
		Diagnostic string `json:"diagnostic"`
		Truncated  bool   `json:"truncated,omitempty"`
	}{StatusUnavailable, string(text[:min(len(text), 2048)]), truncated})
	if err != nil {
		return Delivery{}, err
	}
	parts := []api.MessageContentPart{
		{Content: instructions, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, Source: "agent_policy_status"},
		{Content: string(diagnostic), Origin: api.MessageOriginHost, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted, Source: "agent_policy_diagnostic"},
	}
	return Delivery{Status: StatusUnavailable, Messages: []api.Message{{
		Role: api.MessageRoleSystem, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem,
		TrustTier: api.ContentTrustTierTrusted, Content: instructions + "\n\n" + string(diagnostic), ContentParts: parts,
	}}}, nil
}
