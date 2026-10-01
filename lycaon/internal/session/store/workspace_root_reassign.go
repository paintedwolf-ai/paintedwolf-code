package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// ReassignSessionsWorkspaceRoot retargets sessions from one root.
func (s *SQL) ReassignSessionsWorkspaceRoot(ctx context.Context, projectID, fromRootID, toRootID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	projectID = strings.TrimSpace(projectID)
	fromRootID = strings.TrimSpace(fromRootID)
	if projectID == "" || fromRootID == "" {
		return nil
	}
	var replacement sql.NullString
	if trimmed := strings.TrimSpace(toRootID); trimmed != "" {
		replacement = db.NullString(trimmed)
	}
	q := db.New(s.db)
	return q.ReassignSessionsWorkspaceRoot(ctx, db.ReassignSessionsWorkspaceRootParams{
		WorkspaceRootID:   replacement,
		UpdatedAt:         db.FormatTime(time.Now().UTC()),
		ProjectID:         projectID,
		WorkspaceRootID_2: db.NullString(fromRootID),
	})
}
