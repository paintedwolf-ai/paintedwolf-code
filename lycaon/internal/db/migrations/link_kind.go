package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// migrateLinkKind rebuilds the four source tables with link entries admitted.
func migrateLinkKind(ctx context.Context, tx *sql.Tx) error {
	triggers, err := suspendLinkTableTriggers(ctx, tx)
	if err != nil {
		return err
	}
	views, err := suspendLinkTableViews(ctx, tx)
	if err != nil {
		return err
	}
	for _, table := range []string{"source_files", "source_versions", "source_head_entries", "source_effects"} {
		if err := rebuildLinkTable(ctx, tx, table); err != nil {
			return fmt.Errorf("rebuild %s for link entries: %w", table, err)
		}
	}
	for _, definition := range append(views, triggers...) {
		if _, err := tx.ExecContext(ctx, definition); err != nil {
			return fmt.Errorf("restore source trigger: %w", err)
		}
	}
	return nil
}

func suspendLinkTableViews(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='view' AND sql IS NOT NULL ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type view struct{ name, definition string }
	var views []view
	for rows.Next() {
		var item view
		if err := rows.Scan(&item.name, &item.definition); err != nil {
			return nil, err
		}
		views = append(views, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	definitions := make([]string, 0, len(views))
	for _, item := range views {
		if _, err := tx.ExecContext(ctx, `DROP VIEW "`+strings.ReplaceAll(item.name, `"`, `""`)+`"`); err != nil {
			return nil, err
		}
		definitions = append(definitions, item.definition)
	}
	return definitions, nil
}

func suspendLinkTableTriggers(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='trigger' AND sql IS NOT NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type trigger struct{ name, definition string }
	var triggers []trigger
	for rows.Next() {
		var item trigger
		if err := rows.Scan(&item.name, &item.definition); err != nil {
			return nil, err
		}
		triggers = append(triggers, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	definitions := make([]string, 0, len(triggers))
	for _, item := range triggers {
		if !strings.Contains(item.definition, "source_files") && !strings.Contains(item.definition, "source_versions") &&
			!strings.Contains(item.definition, "source_head_entries") && !strings.Contains(item.definition, "source_effects") {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(item.name, `"`, `""`)+`"`); err != nil {
			return nil, err
		}
		definitions = append(definitions, item.definition)
	}
	return definitions, nil
}

func rebuildLinkTable(ctx context.Context, tx *sql.Tx, table string) error {
	var ddl string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
		return err
	}
	open := strings.IndexByte(ddl, '(')
	if open < 0 {
		return fmt.Errorf("source table has no definition")
	}
	tail := ddl[open:]
	switch table {
	case "source_files", "source_effects":
		if strings.Count(tail, "entry_kind IN ('file', 'directory')") != 1 {
			return fmt.Errorf("source entry kind differs from revision 10 shape")
		}
		tail = strings.Replace(tail, "entry_kind IN ('file', 'directory')", "entry_kind IN ('file', 'directory', 'link')", 1)
	case "source_versions", "source_head_entries":
		if strings.Count(tail, "state IN ('content', 'directory', 'absent', 'unresolved')") != 1 {
			return fmt.Errorf("source state differs from revision 10 shape")
		}
		tail = strings.Replace(tail, "state IN ('content', 'directory', 'absent', 'unresolved')", "state IN ('content', 'directory', 'link', 'absent', 'unresolved')", 1)
		if table == "source_versions" {
			if strings.Count(tail, "state = 'content' AND") != 1 {
				return fmt.Errorf("source content constraint differs from revision 10 shape")
			}
			tail = strings.Replace(tail, "state = 'content' AND", "state IN ('content', 'link') AND", 1)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE tbl_name=? AND type='index' AND sql IS NOT NULL ORDER BY name`, table)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var definitions []string
	for rows.Next() {
		var definition string
		if err := rows.Scan(&definition); err != nil {
			return err
		}
		definitions = append(definitions, definition)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	temporary := table + "_link_rebuild"
	statement := fmt.Sprintf(string(linkTableRebuildSQL), temporary, table, tail)
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return err
	}
	for _, definition := range definitions {
		if _, err := tx.ExecContext(ctx, definition); err != nil {
			return err
		}
	}
	return nil
}
