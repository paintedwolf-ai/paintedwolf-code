package secretcap

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
)

const (
	UseResolved        = "resolved"
	UseRevoked         = "revoked"
	UseAgentUseExpired = "agent_use_expired"
	UseUnavailable     = "unavailable"
	UseOutOfScope      = "out_of_scope"

	maxUseHistory = 200
)

// Use records a value-free substitution attempt.
type Use struct {
	ToolCallID    string `json:"tool_call_id,omitempty"`
	Delivery      string `json:"delivery"`
	UsedAt        string `json:"used_at"`
	ToolName      string `json:"tool_name"`
	Outcome       string `json:"outcome"`
	Version       int64  `json:"version,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	ChatSessionID string `json:"chat_session_id,omitempty"`
	// Recipients names the reviewed release that handed the value off;
	// UnlockID the unlock a person-held value left under.
	Recipients []UseRecipient `json:"recipients"`
	UnlockID   string         `json:"unlock_id,omitempty"`
}

// UseHistory is one capability's recent uses plus how many the window holds.
type UseHistory struct {
	Items []Use `json:"items"`
	Count int   `json:"count"`
}

// Uses returns a capability's recent substitution attempts, newest first.
func (s *Service) Uses(ctx context.Context, projectID, reference string, limit int) (UseHistory, error) {
	row, err := s.projectRow(ctx, projectID, reference)
	if err != nil {
		return UseHistory{}, err
	}
	if limit <= 0 || limit > maxUseHistory {
		limit = maxUseHistory
	}
	rows, err := s.queries.ListManagedSecretUses(ctx, db.ListManagedSecretUsesParams{
		SecretID: row.ID, LimitCount: int64(limit),
	})
	if err != nil {
		return UseHistory{}, err
	}
	out := UseHistory{Items: make([]Use, 0, len(rows)), Count: len(rows)}
	for _, use := range rows {
		item := Use{
			UsedAt: use.UsedAt, ToolName: use.ToolName, Outcome: use.Outcome, ToolCallID: use.ToolCallID.String,
			Delivery: use.Delivery, Recipients: decodeUseRecipients(use.RecipientsJson),
			UnlockID: use.UnlockID.String,
		}
		if use.Version.Valid {
			item.Version = use.Version.Int64
		}
		if use.SessionID.Valid {
			item.SessionID = use.SessionID.String
		}
		if use.ChatSessionID.Valid {
			item.ChatSessionID = use.ChatSessionID.String
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// recordUse stores one attempt and returns its id. A failed write is logged
// and leaves the resolution result unchanged.
func (s *Service) recordUse(ctx context.Context, secretID string, version int64, outcome string, access ResolveContext) string {
	if s == nil || strings.TrimSpace(secretID) == "" {
		return ""
	}
	toolName := strings.TrimSpace(access.ToolName)
	if toolName == "" {
		toolName = "unknown"
	}
	id := uuid.NewString()
	delivery := DeliveryNotDispatched
	if outcome == UseResolved {
		delivery = DeliveryPending
	}
	err := s.queries.CreateManagedSecretUse(ctx, db.CreateManagedSecretUseParams{
		ID: id, ToolCallID: nullable(access.ToolCallID), Delivery: delivery, SecretID: secretID, Version: nullableInt(version),
		ToolName: toolName, SessionID: nullable(access.SessionID),
		ChatSessionID: nullable(access.ChatSessionID), Outcome: outcome,
		UsedAt: db.FormatTime(s.now()),
	})
	if err != nil {
		slog.WarnContext(ctx, "managed secret history write failed", "secret_id", secretID, "tool_call_id", access.ToolCallID)
		return ""
	}
	err = s.queries.TrimManagedSecretUses(ctx, db.TrimManagedSecretUsesParams{
		SecretID: secretID, Keep: maxUseHistory,
	})
	if err != nil {
		slog.WarnContext(ctx, "managed secret history retention failed", "secret_id", secretID)
	}
	return id
}
