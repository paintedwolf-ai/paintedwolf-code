package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// DeleteSessionTree removes a session tree and rows without foreign-key cascades.
func DeleteSessionTree(ctx context.Context, tx *sql.Tx, rootID string) ([]string, error) {
	rootID = strings.TrimSpace(rootID)
	if rootID == "" {
		return nil, nil
	}
	ids, err := SessionTreeIDs(ctx, tx, rootID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	for _, id := range ids {
		if err := deleteSessionOwnedRows(ctx, tx, id, now); err != nil {
			return nil, err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, rootID)
	if err != nil {
		return nil, fmt.Errorf("delete session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return ids, nil
}

// sessionTreeQuery returns a root session and its descendants.
const sessionTreeQuery = `
		WITH RECURSIVE session_tree(id) AS (
		    SELECT sessions.id FROM sessions WHERE sessions.id = ?
		    UNION ALL
		    SELECT s.id FROM sessions s
		    JOIN session_tree parent ON s.parent_session_id = parent.id
		)
		SELECT session_tree.id FROM session_tree`

// SessionTreeIDs returns rootID followed by every descendant session id.
func SessionTreeIDs(ctx context.Context, tx *sql.Tx, rootID string) ([]string, error) {
	return queryStringColumn(ctx, tx, sessionTreeQuery, rootID)
}

// deleteSessionOwnedRows removes one session's rows that outlive it by default.
func deleteSessionOwnedRows(ctx context.Context, tx *sql.Tx, sessionID string, now time.Time) error {
	// Tombstone the search projection while preserving stable evidence handles.
	if _, err := tx.ExecContext(ctx, `
		UPDATE evidence_index
		SET tombstoned = 1,
		    snippet = NULL,
		    path = NULL,
		    line = NULL,
		    url = NULL,
		    source_ref = NULL,
		    check_id = NULL
		WHERE session_id = ? AND tombstoned = 0
	`, sessionID); err != nil {
		return fmt.Errorf("tombstone session search projection: %w", err)
	}
	// Revoke grants before deleting their authorizing session.
	if _, err := RevokeBlueprintApprovalsForSession(ctx, tx, sessionID, now); err != nil {
		return err
	}
	runIDs, err := queryStringColumn(ctx, tx, `SELECT id FROM workflow_runs WHERE session_id = ?`, sessionID)
	if err != nil {
		return err
	}
	return deleteWorkflowRuns(ctx, tx, runIDs)
}

// deleteWorkflowRuns deletes a session's workflow runs and their messages.
// Child runs share their parent's session, so runIDs is a closed set.
// Messages go first: deleting runs sets their workflow_run_id to NULL.
func deleteWorkflowRuns(ctx context.Context, tx *sql.Tx, runIDs []string) error {
	if len(runIDs) == 0 {
		return nil
	}
	placeholders := INPlaceholders(len(runIDs))
	// #nosec G202 -- placeholders is a generated "?,?,..." string, not
	// interpolated input; every id is bound through stringArgs.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM session_entries
		WHERE id IN (
		    SELECT message.id FROM messages message
		    WHERE message.workflow_run_id IN (`+placeholders+`)
		)
	`, stringArgs(runIDs)...); err != nil {
		return fmt.Errorf("delete workflow run messages: %w", err)
	}
	// #nosec G202 -- see above.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM workflow_runs WHERE id IN (`+placeholders+`)
	`, stringArgs(runIDs)...); err != nil {
		return fmt.Errorf("delete workflow_runs: %w", err)
	}
	return nil
}

func queryStringColumn(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func stringArgs(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
