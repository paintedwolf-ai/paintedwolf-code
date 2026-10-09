package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// SessionScaffoldSQLStore implements SessionScaffoldStore against SQLite.
type SessionScaffoldSQLStore struct {
	queries *db.Queries
}

// workflowpersistence.NewSessionScaffoldSQLStore creates a session workflow scaffold store.
func NewSessionScaffoldSQLStore(database db.Handle) *SessionScaffoldSQLStore {
	return &SessionScaffoldSQLStore{queries: db.New(database)}
}

// GetVars returns session workflow scaffold vars.
func (s *SessionScaffoldSQLStore) GetVars(ctx context.Context, sessionID string) (map[string]any, error) {
	if s == nil || s.queries == nil {
		return map[string]any{}, nil
	}
	raw, err := s.queries.GetSessionScaffoldVars(ctx, sessionID)
	if db.IsNoRows(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	vars := map[string]any{}
	if strings.TrimSpace(raw) == "" || raw == "{}" {
		return vars, nil
	}
	if err := json.Unmarshal([]byte(raw), &vars); err != nil {
		return nil, fmt.Errorf("decode session workflow scaffold: %w", err)
	}
	return vars, nil
}

// UpsertVars persists session workflow scaffold vars.
func (s *SessionScaffoldSQLStore) UpsertVars(ctx context.Context, sessionID string, vars map[string]any) error {
	if s == nil || s.queries == nil {
		return nil
	}
	if vars == nil {
		vars = map[string]any{}
	}
	payload, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	return s.queries.UpsertSessionScaffoldVars(ctx, db.UpsertSessionScaffoldVarsParams{
		SessionID: sessionID,
		VarsJson:  string(payload),
		UpdatedAt: db.FormatTime(time.Now().UTC()),
	})
}
