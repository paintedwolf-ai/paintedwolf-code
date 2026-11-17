package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// suspendViews drops every view so a rebuilt table can be renamed into place;
// it returns the definitions to restore.
func suspendViews(ctx context.Context, tx *sql.Tx) ([]string, error) {
	return suspendSchemaObjects(ctx, tx, `SELECT name,sql FROM sqlite_master WHERE type='view' AND sql IS NOT NULL ORDER BY rowid`, "VIEW", nil)
}

// suspendTriggers drops the triggers whose definitions name any of tables.
func suspendTriggers(ctx context.Context, tx *sql.Tx, tables ...string) ([]string, error) {
	return suspendSchemaObjects(ctx, tx, `SELECT name,sql FROM sqlite_master WHERE type='trigger' AND sql IS NOT NULL ORDER BY name`, "TRIGGER", func(definition string) bool {
		for _, table := range tables {
			if strings.Contains(definition, table) {
				return true
			}
		}
		return false
	})
}

func suspendSchemaObjects(ctx context.Context, tx *sql.Tx, query, kind string, keep func(string) bool) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type object struct{ name, definition string }
	var objects []object
	for rows.Next() {
		var item object
		if err := rows.Scan(&item.name, &item.definition); err != nil {
			return nil, err
		}
		if keep == nil || keep(item.definition) {
			objects = append(objects, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	definitions := make([]string, 0, len(objects))
	for _, item := range objects {
		if _, err := tx.ExecContext(ctx, `DROP `+kind+` "`+strings.ReplaceAll(item.name, `"`, `""`)+`"`); err != nil {
			return nil, err
		}
		definitions = append(definitions, item.definition)
	}
	return definitions, nil
}

func restoreSchemaObjects(ctx context.Context, tx *sql.Tx, definitions []string) error {
	for _, definition := range definitions {
		if _, err := tx.ExecContext(ctx, definition); err != nil {
			return fmt.Errorf("restore schema object: %w", err)
		}
	}
	return nil
}

// rebuildTable recreates table from its stored definition after edit rewrites
// the column list, keeping every row and index.
func rebuildTable(ctx context.Context, tx *sql.Tx, table string, edit func(tail string) (string, error)) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
		return err
	}
	open := strings.IndexByte(ddl, '(')
	if open < 0 {
		return fmt.Errorf("%s has no definition", table)
	}
	tail, err := edit(ddl[open:])
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE tbl_name=? AND type='index' AND sql IS NOT NULL ORDER BY name`, table)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var indexes []string
	for rows.Next() {
		var definition string
		if err := rows.Scan(&definition); err != nil {
			return err
		}
		indexes = append(indexes, definition)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	statement := fmt.Sprintf(string(tableRebuildSQL), table+"_rebuild", table, tail)
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return err
	}
	return restoreSchemaObjects(ctx, tx, indexes)
}

// replaceOnce rewrites the single expected occurrence of old in a definition.
func replaceOnce(definition, old, replacement string) (string, error) {
	if strings.Count(definition, old) != 1 {
		return "", fmt.Errorf("definition differs from its expected shape at %q", old)
	}
	return strings.Replace(definition, old, replacement, 1), nil
}
