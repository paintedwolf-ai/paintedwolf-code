package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"math"
)

//go:embed tool_result_file_edits.go
var toolResultFileEditsImplementation []byte

// toolResultFileEdit selects tool results that still hold the single edit.
const toolResultFileEdit = `role = 'tool' AND json_type(NULLIF(tool_result_json, ''), '$.file_edit')`

// ToolResultFileEdits moves each tool result's single file edit, and the
// redaction spans that name its fields, to the first place of the edits list.
func ToolResultFileEdits() Part {
	return Part{
		Source: toolResultFileEditsImplementation, Apply: migrateToolResultFileEdits,
		Validate: validateToolResultFileEdits, Writes: toolResultFileEditWrites,
	}
}

func migrateToolResultFileEdits(ctx context.Context, tx *sql.Tx) error {
	after, started := "", false
	for {
		batch, err := readToolResultFileEditBatch(ctx, tx, started, after)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, id := range batch {
			// A null edit holds nothing and is removed.
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET
  tool_result_json = CASE json_type(tool_result_json, '$.file_edit')
    WHEN 'object' THEN json_insert(json_remove(tool_result_json, '$.file_edit'), '$.file_edits', json_array(json(json_extract(tool_result_json, '$.file_edit'))))
    WHEN 'null' THEN json_remove(tool_result_json, '$.file_edit')
    ELSE tool_result_json END,
  host_secret_redaction_json = CASE json_type(tool_result_json, '$.file_edit')
    WHEN 'object' THEN replace(host_secret_redaction_json, '"tool_result.file_edit.', '"tool_result.file_edits.0.')
    ELSE host_secret_redaction_json END
 WHERE id = ?`, id); err != nil {
				return err
			}
		}
		after, started = batch[len(batch)-1], true
	}
}

func readToolResultFileEditBatch(ctx context.Context, tx *sql.Tx, started bool, after string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM messages WHERE `+toolResultFileEdit+` IN ('object', 'null')
  AND (? = 0 OR id > ?) ORDER BY id LIMIT 256`, started, after)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	batch := make([]string, 0, 256)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		batch = append(batch, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return batch, rows.Close()
}

// A single edit of any other shape is unknown; the store is left as it was.
func validateToolResultFileEdits(ctx context.Context, tx *sql.Tx) error {
	var remaining int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE `+toolResultFileEdit+` IS NOT NULL`).Scan(&remaining); err != nil {
		return err
	}
	if remaining != 0 {
		return fmt.Errorf("%d tool results hold a file edit of an unknown shape", remaining)
	}
	return nil
}

const (
	// A rewritten row frees its old pages, writes new ones and reindexes its text.
	toolResultFileEditRowScale = 4
	toolResultFileEditRowBytes = 16 << 10
)

func toolResultFileEditWrites(ctx context.Context, store DBTX, _ uint64) (uint64, error) {
	var rows, text int64
	if err := store.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(
 length(CAST(tool_result_json AS BLOB)) + length(CAST(COALESCE(host_secret_redaction_json, '') AS BLOB)) +
 length(CAST(COALESCE(content, '') AS BLOB))), 0)
 FROM messages WHERE `+toolResultFileEdit+` IS NOT NULL`).Scan(&rows, &text); err != nil {
		return 0, err
	}
	var total uint64
	for _, part := range []struct {
		count int64
		scale uint64
	}{{rows, toolResultFileEditRowBytes}, {text, toolResultFileEditRowScale}} {
		if part.count < 0 || uint64(part.count) > (math.MaxUint64-total)/part.scale {
			return 0, fmt.Errorf("file edit migration write estimate overflows")
		}
		total += uint64(part.count) * part.scale
	}
	return total, nil
}
