package hitl

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// ChatGrant is one approval delta whose authority lasts for its chat.
type ChatGrant struct {
	ChatSessionID string
	ID            string
	CheckpointID  string
	Delta         ApprovalAuthorityDelta
	// ExpiresAt is fixed when the approval commits; nil lasts for the chat.
	ExpiresAt *time.Time
}

// ChatLifetime reports the chat a delta's authority lasts for and the id
// Saved approvals revokes it by. Current-action, project, and device authority
// are not chat grants.
func (d ApprovalAuthorityDelta) ChatLifetime() (chat, id string, ok bool) {
	switch d.Kind {
	case AuthoritySocketChat, AuthorityDirectIPChat, AuthorityWriteRootChat,
		AuthorityReadPathChat, AuthorityLocalListenChat, AuthorityLoopbackConnectChat:
		if d.Grant != nil {
			chat, id = d.ChatSession(), d.Grant.ID
		}
	case AuthorityGenericGrant, AuthorityGrantedPath:
		if d.Grant != nil && d.Grant.Scope == ApprovalGrantScopeChat {
			chat, id = firstNonEmpty(d.Grant.ChatSessionID, d.ChatSession()), d.Grant.ID
		}
	case AuthorityAskQuiet:
		if d.AskQuiet != nil {
			chat, id = d.ChatSession(), d.AskQuiet.ID
		}
	case AuthorityCurrentAction, AuthoritySocketPermit, AuthorityDirectIPPermit, AuthorityTrustDestination:
	}
	chat, id = strings.TrimSpace(chat), strings.TrimSpace(id)
	return chat, id, chat != "" && id != ""
}

// chatExpiry fixes a delta's relative lifetime to an instant at approval.
func (d ApprovalAuthorityDelta) chatExpiry(approvedAt time.Time) *time.Time {
	var expires *time.Time
	if d.Grant != nil {
		expires = d.Grant.ResolveExpiry(approvedAt)
	}
	if d.TTLSeconds > 0 {
		ttl := approvedAt.UTC().Add(time.Duration(d.TTLSeconds) * time.Second)
		if expires == nil || ttl.Before(*expires) {
			expires = &ttl
		}
	}
	return expires
}

// rebased returns the delta with its remaining lifetime at now, or false when
// it has expired.
func (g ChatGrant) rebased(now time.Time) (ApprovalAuthorityDelta, bool) {
	delta := g.Delta
	if g.ExpiresAt == nil {
		return delta, true
	}
	remaining := g.ExpiresAt.Sub(now)
	if remaining <= 0 {
		return delta, false
	}
	seconds := int(math.Ceil(remaining.Seconds()))
	if delta.TTLSeconds > 0 {
		delta.TTLSeconds = seconds
	}
	if delta.Grant != nil {
		grant := *delta.Grant
		if grant.TTLSeconds > 0 {
			grant.TTLSeconds = seconds
		}
		delta.Grant = &grant
	}
	return delta, true
}

// recordChatGrantsTx writes an option's chat grants in the approval's commit.
func recordChatGrantsTx(ctx context.Context, tx *sql.Tx, checkpointID string, option ApprovalOption, approvedAt time.Time) error {
	queries := db.New(tx)
	for _, delta := range option.Authority {
		chat, id, ok := delta.ChatLifetime()
		if !ok {
			continue
		}
		raw, err := json.Marshal(delta)
		if err != nil {
			return fmt.Errorf("encode chat grant %s: %w", id, err)
		}
		var expiresAt sql.NullString
		if expires := delta.chatExpiry(approvedAt); expires != nil {
			expiresAt = db.NullString(db.FormatTime(*expires))
		}
		if err := queries.UpsertChatGrant(ctx, db.UpsertChatGrantParams{
			ChatSessionID: chat, ID: id, CheckpointID: checkpointID,
			DeltaJson: string(raw), CreatedAt: db.FormatTime(approvedAt), ExpiresAt: expiresAt,
		}); err != nil {
			return fmt.Errorf("record chat grant %s: %w", id, err)
		}
	}
	return nil
}

func (s *SQLStore) chatGrants(ctx context.Context, now time.Time) ([]ChatGrant, error) {
	if err := s.queries.DeleteExpiredChatGrants(ctx, db.NullString(db.FormatTime(now))); err != nil {
		return nil, fmt.Errorf("prune expired chat grants: %w", err)
	}
	rows, err := s.queries.ListChatGrants(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ChatGrant, 0, len(rows))
	for _, row := range rows {
		grant := ChatGrant{ChatSessionID: row.ChatSessionID, ID: row.ID, CheckpointID: row.CheckpointID}
		if err := json.Unmarshal([]byte(row.DeltaJson), &grant.Delta); err != nil {
			return nil, fmt.Errorf("decode chat grant %s: %w", row.ID, err)
		}
		if grant.Delta.Grant != nil {
			if err := ValidateElevatedEffects(grant.Delta.Grant.ElevatedEffects); err != nil {
				return nil, fmt.Errorf("decode chat grant %s: %w", row.ID, err)
			}
		}
		if grant.Delta.AskQuiet != nil {
			if err := ValidateElevatedEffects(grant.Delta.AskQuiet.ElevatedEffects); err != nil {
				return nil, fmt.Errorf("decode chat quiet %s: %w", row.ID, err)
			}
		}
		if row.ExpiresAt.Valid {
			expires, err := db.ParseTime(row.ExpiresAt.String)
			if err != nil {
				return nil, fmt.Errorf("decode chat grant %s expiry: %w", row.ID, err)
			}
			grant.ExpiresAt = &expires
		}
		out = append(out, grant)
	}
	return out, nil
}

func (s *SQLStore) forgetChatGrant(ctx context.Context, id string) (bool, error) {
	rows, err := s.queries.DeleteChatGrant(ctx, id)
	return rows > 0, err
}

// RestoreChatGrants replays each chat's live approvals into the runtime
// stores at boot. A grant that no longer installs is dropped from the ledger,
// so the next matching action asks again.
func (m *Manager) RestoreChatGrants(ctx context.Context) error {
	release := m.LockApprovalAuthority()
	defer release()
	now := time.Now().UTC()
	grants, err := m.store.chatGrants(ctx, now)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		delta, live := grant.rebased(now)
		if !live {
			continue
		}
		option := ApprovalOption{Authority: []ApprovalAuthorityDelta{delta}}
		if _, err := m.authorityInstaller.InstallApprovalOption(ctx, grant.CheckpointID, option); err != nil {
			slog.WarnContext(ctx, "chat grant no longer installs", "chat_session_id", grant.ChatSessionID, "grant_id", grant.ID, "error", err)
			if _, forgetErr := m.store.forgetChatGrant(ctx, grant.ID); forgetErr != nil {
				return forgetErr
			}
		}
	}
	return nil
}

// ForgetChatGrant removes a revoked chat grant from the ledger.
func (m *Manager) ForgetChatGrant(ctx context.Context, id string) (bool, error) {
	return m.store.forgetChatGrant(ctx, strings.TrimSpace(id))
}

// LockApprovalAuthority serializes install-and-seal with saved authority revocation.
func (m *Manager) LockApprovalAuthority() func() {
	m.authorityMutation.Lock()
	return m.authorityMutation.Unlock
}
