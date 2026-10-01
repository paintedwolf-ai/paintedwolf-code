package progress

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// SQLStore is the durable Store backed by the session_progress table.
type SQLStore struct {
	queries *db.Queries
}

var _ RunScopedStore = (*SQLStore)(nil)

// NewSQLStore returns a progress store backed by database.
func NewSQLStore(database db.Handle) *SQLStore {
	return &SQLStore{queries: db.New(database)}
}

func (s *SQLStore) Set(sessionID, content string) error {
	if s == nil || s.queries == nil {
		return fmt.Errorf("progress store not configured")
	}
	key := normalizeKey(sessionID)
	if key == "" {
		return fmt.Errorf("session id required")
	}
	runID := s.BoundRunID(key)
	return s.queries.UpsertSessionProgressContent(context.Background(), db.UpsertSessionProgressContentParams{
		SessionID:     key,
		WorkflowRunID: runID,
		Content:       clampContent(content),
		UpdatedAt:     db.FormatTime(time.Now().UTC()),
	})
}

func (s *SQLStore) Get(ctx context.Context, sessionID string) string {
	if s == nil || s.queries == nil {
		return ""
	}
	content, err := s.queries.GetSessionProgressContent(ctx, normalizeKey(sessionID))
	if err != nil {
		return ""
	}
	return content
}

func (s *SQLStore) BoundRunID(sessionID string) string {
	if s == nil || s.queries == nil {
		return ""
	}
	runID, err := s.queries.GetSessionProgressRunID(context.Background(), normalizeKey(sessionID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(runID)
}

func (s *SQLStore) BindRun(sessionID, workflowRunID string) {
	if s == nil || s.queries == nil {
		return
	}
	key := normalizeKey(sessionID)
	workflowRunID = strings.TrimSpace(workflowRunID)
	if key == "" || workflowRunID == "" {
		return
	}
	_ = s.queries.BindSessionProgressRun(context.Background(), db.BindSessionProgressRunParams{
		SessionID:     key,
		WorkflowRunID: workflowRunID,
		UpdatedAt:     db.FormatTime(time.Now().UTC()),
	})
}

func (s *SQLStore) EnsureRun(sessionID, workflowRunID, goal string) (refreshed bool) {
	if s == nil || s.queries == nil {
		return false
	}
	key := normalizeKey(sessionID)
	workflowRunID = strings.TrimSpace(workflowRunID)
	if key == "" || workflowRunID == "" {
		return false
	}
	_ = s.queries.EnsureSessionProgressRun(context.Background(), db.EnsureSessionProgressRunParams{
		SessionID:     key,
		WorkflowRunID: workflowRunID,
		Content:       clampContent(formatBootstrapContent(goal)),
		UpdatedAt:     db.FormatTime(time.Now().UTC()),
	})
	return true
}
