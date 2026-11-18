package db

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// ProjectRowBatch is the most rows one statement of a project's deletion
// removes from a table; triggers and cascades from those rows add to it.
const ProjectRowBatch = 1024

// newestFirst orders a table whose rows reference earlier rows of the same
// project without cascading, so no batch removes a row a remaining row names.
var newestFirst = map[string]string{
	"source_versions":    "seq DESC",
	"source_directories": "rowid DESC",
}

type projectRowTable struct {
	// key is the row's identity: rowid, or the primary key columns.
	name, column, key, order string
}

// keyColumns is the key as a select list.
func (t projectRowTable) keyColumns() string {
	return strings.TrimSuffix(strings.TrimPrefix(t.key, "("), ")")
}

// DeleteProjectRows removes every row that references a project, leaving the
// projects row. Each table is cleared in its own statements of at most
// ProjectRowBatch rows, tables that reference others first, so no statement
// cascades through a project's whole history.
func DeleteProjectRows(ctx context.Context, database DBTX, projectID string) error {
	tables, err := projectRowTables(ctx, database)
	if err != nil {
		return err
	}
	for _, table := range tables {
		statement := fmt.Sprintf(`DELETE FROM %s WHERE %s IN (SELECT %s FROM %s WHERE %s = ? %s LIMIT %d)`, // #nosec G201 -- names read from the schema
			quoteIdent(table.name), table.key, table.keyColumns(), quoteIdent(table.name), quoteIdent(table.column), table.order, ProjectRowBatch)
		for {
			result, err := database.ExecContext(ctx, statement, projectID)
			if err != nil {
				return fmt.Errorf("delete project rows from %s: %w", table.name, err)
			}
			deleted, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if deleted == 0 {
				break
			}
		}
	}
	return nil
}

type foreignKeyRef struct {
	parent, column, onDelete string
}

// projectRowTables reads the store's schema: the tables with a foreign key to
// projects, ordered so a table comes before every table it references.
func projectRowTables(ctx context.Context, database DBTX) ([]projectRowTable, error) {
	names, err := queryStrings(ctx, database, `SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	owned := map[string]projectRowTable{}
	refs := map[string][]foreignKeyRef{}
	for _, name := range names {
		keys, err := foreignKeyRefs(ctx, database, name)
		if err != nil {
			return nil, err
		}
		refs[name] = keys
		for _, key := range keys {
			if key.parent != "projects" {
				continue
			}
			table := projectRowTable{name: name, column: key.column, key: "rowid", order: ""}
			if order, ok := newestFirst[name]; ok {
				table.order = "ORDER BY " + order
			}
			if table.key, err = rowKey(ctx, database, name); err != nil {
				return nil, err
			}
			owned[name] = table
		}
	}
	for name := range owned {
		for _, key := range refs[name] {
			if key.parent == name && key.onDelete != "CASCADE" && key.onDelete != "SET NULL" && newestFirst[name] == "" {
				return nil, fmt.Errorf("table %s references its own rows with no deletion order", name)
			}
		}
	}
	// referencedBy[b] holds the owned tables that still reference b.
	referencedBy := map[string]map[string]bool{}
	for name := range owned {
		referencedBy[name] = map[string]bool{}
	}
	for name := range owned {
		for _, key := range refs[name] {
			if _, ok := owned[key.parent]; ok && key.parent != name {
				referencedBy[key.parent][name] = true
			}
		}
	}
	ordered := make([]projectRowTable, 0, len(owned))
	for len(referencedBy) > 0 {
		var ready []string
		for name, by := range referencedBy {
			if len(by) == 0 {
				ready = append(ready, name)
			}
		}
		if len(ready) == 0 {
			return nil, fmt.Errorf("project tables reference each other in a cycle")
		}
		slices.Sort(ready)
		for _, name := range ready {
			ordered = append(ordered, owned[name])
			delete(referencedBy, name)
			for _, by := range referencedBy {
				delete(by, name)
			}
		}
	}
	return ordered, nil
}

func foreignKeyRefs(ctx context.Context, database DBTX, table string) ([]foreignKeyRef, error) {
	rows, err := database.QueryContext(ctx, `SELECT "table", "from", on_delete FROM pragma_foreign_key_list(?)`, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var refs []foreignKeyRef
	for rows.Next() {
		var ref foreignKeyRef
		if err := rows.Scan(&ref.parent, &ref.column, &ref.onDelete); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// rowKey is rowid, or the primary key of a table without one.
func rowKey(ctx context.Context, database DBTX, table string) (string, error) {
	var withoutRowID int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_list WHERE name = ? AND wr = 1`, table).Scan(&withoutRowID); err != nil {
		return "", err
	}
	if withoutRowID == 0 {
		return "rowid", nil
	}
	columns, err := queryStrings(ctx, database, `SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, table)
	if err != nil {
		return "", err
	}
	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = quoteIdent(column)
	}
	return "(" + strings.Join(quoted, ", ") + ")", nil
}

func queryStrings(ctx context.Context, database DBTX, query string, args ...any) ([]string, error) {
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var values []string
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value.String)
	}
	return values, rows.Err()
}

func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
