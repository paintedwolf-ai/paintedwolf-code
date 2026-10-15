package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"strings"
)

//go:embed tool_result_paths.go
var toolResultPathsImplementation []byte

func migrateToolResultPaths(ctx context.Context, tx *sql.Tx) error {
	after, started := "", false
	for {
		rows, err := tx.QueryContext(ctx, `SELECT e.id, json_extract(m.tool_result_json,'$.tool_args.path')
   FROM evidence_index e JOIN messages m ON m.id=e.message_id AND m.session_id=e.session_id
   WHERE e.source='tool' AND e.hit_kind='tool' AND e.shape='raw' AND m.role='tool' AND (e.path IS NULL OR e.path='')
    AND json_type(NULLIF(m.tool_result_json,''),'$.tool_args.path')='text'
    AND (?=0 OR e.id>?) ORDER BY e.id LIMIT 256`, started, after)
		if err != nil {
			return err
		}
		type target struct{ id, path string }
		batch := make([]target, 0, 256)
		for rows.Next() {
			var row target
			if err := rows.Scan(&row.id, &row.path); err != nil {
				_ = rows.Close()
				return err
			}
			batch = append(batch, row)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
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
