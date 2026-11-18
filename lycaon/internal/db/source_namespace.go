package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// RetargetEditorDocument follows the moving file without displacing an open destination draft.
func RetargetEditorDocument(ctx context.Context, q *Queries, projectID, branchID, fileID, fromRootID, fromPath, toRootID, toPath string, at time.Time) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE editor_documents
		SET root_id = ?, path = ?, revision = revision + 1, updated_at = ?
		WHERE project_id = ? AND branch_id = ? AND file_id = ? AND root_id = ? AND path = ?
		  AND NOT EXISTS (
		    SELECT 1 FROM editor_documents destination
		    WHERE destination.project_id = editor_documents.project_id AND destination.branch_id = editor_documents.branch_id
		      AND destination.root_id = ? AND destination.path = ?
		  )
	`, toRootID, toPath, FormatTime(at), projectID, branchID, fileID, fromRootID, fromPath, toRootID, toPath)
	return err
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
