package search

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// ProjectGateEvidenceComplete upserts gate-evidence rows for one persisted record.
func ProjectGateEvidenceComplete(ctx context.Context, sqlDB db.Handle, in ProjectGateEvidenceInput) error {
	if sqlDB == nil {
		return nil
	}
	rows := ProjectGateEvidence(in)
	if len(rows) == 0 {
		return nil
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := defaultStore.UpsertRows(ctx, tx, rows); err != nil {
		return err
	}
	return tx.Commit()
}

// ResolveProjectIDForSession looks up sessions.project_id for search attribution.
func ResolveProjectIDForSession(ctx context.Context, sqlDB db.Handle, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sqlDB == nil || sessionID == "" {
		return "", nil
	}
	var projectID string
	err := sqlDB.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE id = ?`, sessionID).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(projectID), nil
}
