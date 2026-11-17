package db

import (
	"context"
	"database/sql"
	"errors"
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
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM sessions WHERE id = ?`, rootID).Scan(&projectID); err != nil {
		return nil, fmt.Errorf("session project: %w", err)
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
	if err := ReleaseOutsideFiles(ctx, New(tx), projectID); err != nil {
		return nil, fmt.Errorf("release outside files: %w", err)
	}
	return ids, nil
}

// ReleaseOutsideFiles deletes the history of outside files nothing holds any
// longer, together with operations left without effects or versions.
func ReleaseOutsideFiles(ctx context.Context, q *Queries, projectID string) error {
	released, err := q.ListReleasedOutsideFiles(ctx, projectID)
	if err != nil {
		return err
	}
	for _, fileID := range released {
		operations, err := q.ListSourceFileOperationIDs(ctx, fileID)
		if err != nil {
			return err
		}
		if err := q.DeleteReleasedOutsideFile(ctx, DeleteReleasedOutsideFileParams{FileID: fileID, ProjectID: projectID}); err != nil {
			return err
		}
		for _, operation := range operations {
			if err := q.DeleteEmptySourceOperation(ctx, operation); err != nil {
				return err
			}
		}
	}
	return nil
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

func queryStringColumn(ctx context.Context, tx DBTX, query string, args ...any) ([]string, error) {
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

// AdoptOutsideTree moves the outside files under rel into a newly attached
// root, keeping their file identities, history and editor documents. Recorded
// versions keep the address they were recorded at.
func AdoptOutsideTree(ctx context.Context, q *Queries, projectID, rootID, rel string) error {
	outside, err := q.GetProjectOutsideRootID(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	dir, err := q.GetSourceDirectoryRoot(ctx, GetSourceDirectoryRootParams{ProjectID: projectID, BranchID: "", RootID: outside})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, name := range strings.Split(rel, "/") {
		dir, err = q.GetSourceDirectoryChild(ctx, GetSourceDirectoryChildParams{ParentID: NullString(dir.ID), Name: name})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	subtree, err := queryStringColumn(ctx, q.db, sourceSubtreeQuery, dir.ID)
	if err != nil {
		return err
	}
	for _, id := range subtree {
		if err := q.MoveSourceDirectoryHeadsRoot(ctx, MoveSourceDirectoryHeadsRootParams{RootID: rootID, DirectoryID: id}); err != nil {
			return err
		}
		if err := q.MoveSourceDirectoryRoot(ctx, MoveSourceDirectoryRootParams{RootID: rootID, DirectoryID: id}); err != nil {
			return err
		}
	}
	if err := q.AdoptSourceDirectoryAsRoot(ctx, AdoptSourceDirectoryAsRootParams{DirectoryID: dir.ID, RootID: rootID}); err != nil {
		return err
	}
	return q.MoveOutsideEditorDocuments(ctx, MoveOutsideEditorDocumentsParams{RootID: rootID, Prefix: rel, ProjectID: projectID, OutsideRootID: outside})
}

const sourceSubtreeQuery = `
		WITH RECURSIVE subtree(id) AS (
		    SELECT source_directories.id FROM source_directories WHERE source_directories.id = ?
		    UNION ALL
		    SELECT d.id FROM source_directories d JOIN subtree parent ON d.parent_id = parent.id
		)
		SELECT subtree.id FROM subtree`
