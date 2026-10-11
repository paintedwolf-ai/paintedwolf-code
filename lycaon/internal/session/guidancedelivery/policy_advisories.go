package guidancedelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/policyfeedback"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) DeliverAdvisories(ctx context.Context, sessionID, anchor string, decision *oar.Decision) error {
	if oar.InlineAdvisory(anchor, decision.Effect) {
		return nil
	}
	return m.queuePolicy(ctx, sessionID, anchor, decision)
}

func (m *Service) queuePolicy(_ context.Context, sessionID, anchor string, decision *oar.Decision) error {
	if decision == nil {
		return nil
	}
	if sessionID == "" {
		return fmt.Errorf("policy advisory delivery requires a session")
	}
	entries := make([]guidance.PolicyFeedback, 0, len(decision.Advisories))
	for _, advisory := range decision.Advisories {
		entries = append(entries, guidance.PolicyFeedback{
			Anchor: anchor, Rule: advisory.Rule, Effect: string(decision.Effect), Copy: advisory.Copy,
			ToolFeedback: api.ToolFeedback{Code: advisory.Code, Details: advisory.Data, Subject: policyfeedback.Subject(sessionID, advisory.Data)},
		})
	}
	m.kicks.QueuePolicyFeedback(sessionID, entries)
	return nil
}

// TakePolicy persists before acknowledging. A retry reuses the staged
// message ID, and the loop receives the same screened bytes as durable history.
func (m *Service) TakePolicy(ctx context.Context, sessionID string) ([]api.Message, error) {
	kicks := m.kicks
	lease := kicks.LeasePolicyFeedback(sessionID)
	if len(lease.Entries) == 0 {
		return nil, nil
	}
	msg, err := m.policyMessage(ctx, lease)
	if err != nil {
		return nil, err
	}
	if err := m.transcript.Append(ctx, sessionID, msg); err != nil && !errors.Is(err, store.ErrDuplicateMessageID) {
		return nil, err
	}
	stored, err := m.store.GetMessage(ctx, sessionID, msg.ID)
	if err != nil {
		return nil, err
	}
	if stored.ID == "" {
		return nil, fmt.Errorf("persisted policy feedback %s unavailable", msg.ID)
	}
	kicks.AckPolicyFeedback(sessionID, lease.Sequence)
	return []api.Message{stored}, nil
}

func (m *Service) policyMessage(ctx context.Context, lease kick.PolicyFeedbackLease) (api.Message, error) {
	var parts []api.MessageContentPart
	var content []string
	for _, entry := range lease.Entries {
		if m.renderer == nil {
			return api.Message{}, fmt.Errorf("policy feedback renderer unavailable")
		}
		rendered, err := m.renderer.Render(ctx, oar.StageFromAnchor(entry.Anchor), &oar.Decision{
			Effect: oar.Effect(entry.Effect), Advisories: []oar.Advisory{{Code: entry.Code, Rule: entry.Rule, Copy: entry.Copy, Data: entry.Details}},
		})
		if err != nil {
			return api.Message{}, err
		}
		if len(rendered) != 1 {
			return api.Message{}, fmt.Errorf("policy feedback %s rendered %d entries", entry.Rule, len(rendered))
		}
		text := rendered[0].Text
		raw, err := json.Marshal(entry)
		if err != nil {
			return api.Message{}, fmt.Errorf("encode policy feedback: %w", err)
		}
		parts = append(parts,
			api.MessageContentPart{Content: text, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, Source: "policy_guidance"},
			api.MessageContentPart{Content: string(raw), Origin: api.MessageOriginHost, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted, Source: "policy_feedback"},
		)
		content = append(content, text, string(raw))
	}
	return api.Message{
		ID: lease.ID, Role: api.MessageRoleUser, Content: strings.Join(content, "\n\n"), ContentParts: parts,
		Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		Kind: api.MessageKindCoordinatorGuidance, HostSignalID: "policy_feedback",
		Visibility: api.MessageVisibilityInternal, CreatedAt: time.Now().UTC(),
	}, nil
}
