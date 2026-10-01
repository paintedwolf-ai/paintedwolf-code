package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/pkg/api"
)

var defaultStore = NewStore()

// SyncMessageWriteThrough projects one message write into evidence_index.
func SyncMessageWriteThrough(ctx context.Context, tx *sql.Tx, projectID, sessionID string, msg api.Message) error {
	if err := defaultStore.clearMessageEvidence(ctx, tx, msg.ID); err != nil {
		return err
	}
	toolName := ""
	if msg.ToolResult != nil {
		toolName = msg.ToolResult.Tool
	}
	rows := ProjectMessage(projectID, sessionID, msg)
	rows = append(rows, ProjectMessageText(projectID, sessionID, msg)...)
	rows = append(rows, ProjectToolCalls(projectID, sessionID, msg)...)
	rows = append(rows, ProjectToolMessage(projectID, sessionID, msg, toolName)...)
	rows = append(rows, ProjectLifecycleEvidence(projectID, sessionID, msg)...)
	return defaultStore.UpsertRows(ctx, tx, rows)
}

// rootSessionIDTx walks parent links to the top of the session tree.
func rootSessionIDTx(ctx context.Context, tx *sql.Tx, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", nil
	}
	var root string
	err := tx.QueryRowContext(ctx, `
		WITH RECURSIVE lineage(id, parent_id) AS (
		    SELECT sessions.id, sessions.parent_session_id FROM sessions WHERE sessions.id = ?
		    UNION ALL
		    SELECT s.id, s.parent_session_id FROM sessions s
		    JOIN lineage ON lineage.parent_id = s.id
		)
		SELECT lineage.id FROM lineage WHERE lineage.parent_id IS NULL LIMIT 1
	`, sessionID).Scan(&root)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A row indexed for a session the store does not know is its own root.
		return sessionID, nil
	case err != nil:
		return "", fmt.Errorf("resolve root session: %w", err)
	}
	return root, nil
}

// SyncDraftVersionWriteThrough projects one draft_versions row into evidence_index.
func SyncDraftVersionWriteThrough(ctx context.Context, tx *sql.Tx, projectID, sessionID, slotID string, version api.DraftVersion) error {
	rows := ProjectDraftVersion(projectID, sessionID, slotID, version)
	return defaultStore.UpsertRows(ctx, tx, rows)
}

// SyncUntrustedLedgerWriteThrough projects ledger-only trust markers.
func SyncUntrustedLedgerWriteThrough(ctx context.Context, tx *sql.Tx, projectID, sessionID string, rec evidence.Record) error {
	rows := ProjectUntrustedLedgerRecord(projectID, sessionID, rec)
	return defaultStore.UpsertRows(ctx, tx, rows)
}
