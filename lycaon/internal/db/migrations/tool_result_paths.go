package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"strings"
)

//go:embed tool_result_paths.go
var toolResultPathsImplementation []byte

type toolResultPathTarget struct{ id, path string }

func migrateToolResultPaths(ctx context.Context, tx *sql.Tx) error {
	after, started := "", false
	for {
		batch, err := readToolResultPathBatch(ctx, tx, started, after)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, row := range batch {
			if path := strings.TrimSpace(row.path); path != "" {
				if _, err := tx.ExecContext(ctx, `UPDATE evidence_index SET path=? WHERE id=?`, path, row.id); err != nil {
					return err
				}
			}
		}
		after, started = batch[len(batch)-1].id, true
	}
}

func readToolResultPathBatch(ctx context.Context, tx *sql.Tx, started bool, after string) ([]toolResultPathTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT e.id, json_extract(m.tool_result_json,'$.tool_args.path')
   FROM evidence_index e JOIN messages m ON m.id=e.message_id AND m.session_id=e.session_id
   WHERE e.source='tool' AND e.hit_kind='tool' AND e.shape='raw' AND m.role='tool' AND (e.path IS NULL OR e.path='')
    AND json_type(NULLIF(m.tool_result_json,''),'$.tool_args.path')='text'
    AND (?=0 OR e.id>?) ORDER BY e.id LIMIT 256`, started, after)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	batch := make([]toolResultPathTarget, 0, 256)
	for rows.Next() {
		var row toolResultPathTarget
		if err := rows.Scan(&row.id, &row.path); err != nil {
			return nil, err
		}
		batch = append(batch, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return batch, rows.Close()
}
