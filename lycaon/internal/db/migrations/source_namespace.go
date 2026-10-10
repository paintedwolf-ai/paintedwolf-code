package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"
)

//go:embed source_namespace_schema.sql
var namespaceSchema []byte

//go:embed source_namespace_projection.sql
var namespaceProjection []byte

//go:embed source_namespace.go
var namespaceImplementation []byte

//go:embed review_assignments.sql
var reviewAssignmentsMigration []byte

func migrateSourceNamespace(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE source_branch_heads RENAME TO source_branch_heads_v1`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(namespaceSchema)); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT project_id,branch_id,file_id,version_id,root_id,path,state,content_sha256,ordinal,observed_ts FROM source_branch_heads_v1 ORDER BY project_id,branch_id,root_id,path`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var project, branch, file, version, root, rel, state, sha, ts string
		var ordinal int64
		if err := rows.Scan(&project, &branch, &file, &version, &root, &rel, &state, &sha, &ordinal, &ts); err != nil {
			return err
		}
		parent, err := migrateDirectoryPath(ctx, tx, project, branch, root, path.Dir(rel))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO source_head_entries(project_id,branch_id,file_id,version_id,root_id,directory_id,name,state,content_sha256,ordinal,observed_ts) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, project, branch, file, version, root, parent, path.Base(rel), state, sha, ordinal, ts); err != nil {
			return err
		}
		if state == "directory" {
			if _, err := migrateDirectoryPath(ctx, tx, project, branch, root, rel); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(namespaceProjection)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, string(reviewAssignmentsMigration))
	return err
}

func migrateDirectoryPath(ctx context.Context, tx *sql.Tx, project, branch, root, rel string) (string, error) {
	if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") || path.Clean(rel) != rel {
		return "", fmt.Errorf("invalid source directory %q", rel)
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM source_directories WHERE project_id=? AND branch_id=? AND root_id=? AND parent_id IS NULL`, project, branch, root).Scan(&id)
	if err == sql.ErrNoRows {
		id = uuid.NewString()
		_, err = tx.ExecContext(ctx, `INSERT INTO source_directories(id,project_id,branch_id,root_id,parent_id,name,present,ordinal,observed_ts) VALUES(?,?,?,?,NULL,'',1,0,'')`, id, project, branch, root)
	}
	if err != nil {
		return "", err
	}
	if rel == "." {
		return id, nil
	}
	for _, name := range strings.Split(rel, "/") {
		var next string
		err = tx.QueryRowContext(ctx, `SELECT id FROM source_directories WHERE parent_id=? AND name=? AND present=1`, id, name).Scan(&next)
		if err == sql.ErrNoRows {
			next = uuid.NewString()
			_, err = tx.ExecContext(ctx, `INSERT INTO source_directories(id,project_id,branch_id,root_id,parent_id,name,present,ordinal,observed_ts) VALUES(?,?,?,?,?,?,1,0,'')`, next, project, branch, root, id, name)
		}
		if err != nil {
			return "", err
		}
		id = next
	}
	return id, nil
}

// Validate while both representations exist, then retire the shipped table.
func validateSourceNamespace(ctx context.Context, tx *sql.Tx) error {
	const columns = "project_id,branch_id,file_id,version_id,root_id,path,state,content_sha256,ordinal,observed_ts"
	for _, pair := range [][2]string{{"source_branch_heads_v1", "source_branch_heads"}, {"source_branch_heads", "source_branch_heads_v1"}} {
		var changed bool
		query := "SELECT EXISTS(SELECT " + columns + " FROM " + pair[0] + " EXCEPT SELECT " + columns + " FROM " + pair[1] + ")"
		if err := tx.QueryRowContext(ctx, query).Scan(&changed); err != nil {
			return err
		}
		if changed {
			return fmt.Errorf("source namespace projection differs from released history")
		}
	}
	_, err := tx.ExecContext(ctx, `DROP TABLE source_branch_heads_v1; DROP TABLE source_checkpoint_entries`)
	return err
}
