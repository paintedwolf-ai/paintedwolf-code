package db

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Shape describes the live store structure checked against schema.sql.
// It includes columns, constraints, indexes, triggers, and views.
type Shape struct {
	Columns  map[string][]Column // table name → ordered columns
	Tables   []string            // normalized CREATE TABLE text, sorted by name
	Indexes  []string            // normalized CREATE INDEX text, sorted by name
	Triggers []string            // normalized CREATE TRIGGER text, sorted by name
	Views    []string            // normalized CREATE VIEW text, sorted by name
}

// Column mirrors one PRAGMA table_info row.
type Column struct {
	Name       string
	Type       string
	NotNull    int
	Default    sql.NullString
	PKPosition int
}

// ReadShape reads the live structure of an open store.
func ReadShape(ctx context.Context, db DBTX) (Shape, error) {
	tables, err := listMainNames(ctx, db, "table")
	if err != nil {
		return Shape{}, err
	}
	out := Shape{Columns: make(map[string][]Column, len(tables))}
	for _, table := range tables {
		cols, err := readTableColumns(ctx, db, table)
		if err != nil {
			return Shape{}, err
		}
		out.Columns[table] = cols
	}
	out.Tables, err = listMainSQL(ctx, db, "table")
	if err != nil {
		return Shape{}, err
	}

	indexes, err := listMainSQL(ctx, db, "index")
	if err != nil {
		return Shape{}, err
	}
	out.Indexes = indexes

	triggers, err := listMainSQL(ctx, db, "trigger")
	if err != nil {
		return Shape{}, err
	}
	out.Triggers = triggers

	views, err := listMainSQL(ctx, db, "view")
	if err != nil {
		return Shape{}, err
	}
	out.Views = views
	return out, nil
}

const maxShapeDiffLines = 20

// Diff returns human-readable differences, empty when the shapes match. The
// receiver is described as "want" and the argument as "got"; callers name which
// store each came from.
func (want Shape) Diff(got Shape) []string {
	var diff []string
	appendDiff := func(line string) {
		if len(diff) < maxShapeDiffLines {
			diff = append(diff, line)
		}
	}

	wantTables := sortedKeys(want.Columns)
	gotTables := sortedKeys(got.Columns)
	wantSet := make(map[string]struct{}, len(wantTables))
	for _, table := range wantTables {
		wantSet[table] = struct{}{}
		gotCols, ok := got.Columns[table]
		if !ok {
			appendDiff(fmt.Sprintf("table %s present in want, missing in got", table))
			continue
		}
		diffColumns(table, want.Columns[table], gotCols, appendDiff)
	}
	for _, table := range gotTables {
		if _, ok := wantSet[table]; !ok {
			appendDiff(fmt.Sprintf("table %s present in got, missing in want", table))
		}
	}

	diffNamedSQL("table definition", want.Tables, got.Tables, appendDiff)
	diffNamedSQL("index", want.Indexes, got.Indexes, appendDiff)
	diffNamedSQL("trigger", want.Triggers, got.Triggers, appendDiff)
	diffNamedSQL("view", want.Views, got.Views, appendDiff)

	if total := shapeDiffTotal(want, got); total > maxShapeDiffLines {
		diff = append(diff, fmt.Sprintf("… and %d more", total-maxShapeDiffLines))
	}
	return diff
}

func diffColumns(table string, wantCols, gotCols []Column, appendDiff func(string)) {
	gotByName := make(map[string]Column, len(gotCols))
	for _, col := range gotCols {
		gotByName[col.Name] = col
	}
	seen := make(map[string]struct{}, len(wantCols))
	for _, wantCol := range wantCols {
		seen[wantCol.Name] = struct{}{}
		gotCol, ok := gotByName[wantCol.Name]
		if !ok {
			appendDiff(fmt.Sprintf("table %s: column %s present in want, missing in got", table, wantCol.Name))
			continue
		}
		if wantCol.Type != gotCol.Type {
			appendDiff(fmt.Sprintf("table %s: column %s type %s != %s", table, wantCol.Name, wantCol.Type, gotCol.Type))
		}
		if wantCol.NotNull != gotCol.NotNull {
			appendDiff(fmt.Sprintf("table %s: column %s notnull %d != %d", table, wantCol.Name, wantCol.NotNull, gotCol.NotNull))
		}
		if wantCol.Default.Valid != gotCol.Default.Valid || wantCol.Default.String != gotCol.Default.String {
			appendDiff(fmt.Sprintf("table %s: column %s default %q != %q", table, wantCol.Name, nullString(wantCol.Default), nullString(gotCol.Default)))
		}
		if wantCol.PKPosition != gotCol.PKPosition {
			appendDiff(fmt.Sprintf("table %s: column %s pk %d != %d", table, wantCol.Name, wantCol.PKPosition, gotCol.PKPosition))
		}
	}
	for _, gotCol := range gotCols {
		if _, ok := seen[gotCol.Name]; !ok {
			appendDiff(fmt.Sprintf("table %s: column %s present in got, missing in want", table, gotCol.Name))
		}
	}
}

func diffNamedSQL(kind string, wantSQL, gotSQL []string, appendDiff func(string)) {
	wantByName := namedSQLMap(wantSQL)
	gotByName := namedSQLMap(gotSQL)
	names := make([]string, 0, len(wantByName)+len(gotByName))
	seen := make(map[string]struct{}, len(wantByName)+len(gotByName))
	for name := range wantByName {
		names = append(names, name)
		seen[name] = struct{}{}
	}
	for name := range gotByName {
		if _, ok := seen[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		wantText, wantOK := wantByName[name]
		gotText, gotOK := gotByName[name]
		switch {
		case wantOK && !gotOK:
			appendDiff(fmt.Sprintf("%s %s present in want, missing in got", kind, name))
		case !wantOK && gotOK:
			appendDiff(fmt.Sprintf("%s %s present in got, missing in want", kind, name))
		case wantText != gotText:
			appendDiff(fmt.Sprintf("%s %s sql differs", kind, name))
		}
	}
}

func namedSQLMap(normalized []string) map[string]string {
	out := make(map[string]string, len(normalized))
	for _, sqlText := range normalized {
		name := objectNameFromSQL(sqlText)
		if name == "" {
			name = sqlText
		}
		out[name] = sqlText
	}
	return out
}

func objectNameFromSQL(sqlText string) string {
	fields := strings.Fields(sqlText)
	// CREATE [UNIQUE] INDEX|TRIGGER|VIEW [IF NOT EXISTS] name …
	i := 0
	if i < len(fields) && strings.EqualFold(fields[i], "CREATE") {
		i++
	}
	if i < len(fields) && strings.EqualFold(fields[i], "UNIQUE") {
		i++
	}
	if i < len(fields) {
		switch strings.ToUpper(fields[i]) {
		case "INDEX", "TRIGGER", "VIEW", "TABLE":
			i++
		}
	}
	if i+2 < len(fields) && strings.EqualFold(fields[i], "IF") && strings.EqualFold(fields[i+1], "NOT") && strings.EqualFold(fields[i+2], "EXISTS") {
		i += 3
	}
	if i >= len(fields) {
		return ""
	}
	return strings.Trim(fields[i], `"'[]`)
}

func shapeDiffTotal(want, got Shape) int {
	var n int
	count := func(_ string) { n++ }
	wantTables := sortedKeys(want.Columns)
	gotTables := sortedKeys(got.Columns)
	wantSet := make(map[string]struct{}, len(wantTables))
	for _, table := range wantTables {
		wantSet[table] = struct{}{}
		gotCols, ok := got.Columns[table]
		if !ok {
			count("")
			continue
		}
		diffColumns(table, want.Columns[table], gotCols, count)
	}
	for _, table := range gotTables {
		if _, ok := wantSet[table]; !ok {
			count("")
		}
	}
	diffNamedSQL("table definition", want.Tables, got.Tables, count)
	diffNamedSQL("index", want.Indexes, got.Indexes, count)
	diffNamedSQL("trigger", want.Triggers, got.Triggers, count)
	diffNamedSQL("view", want.Views, got.Views, count)
	return n
}

func listMainNames(ctx context.Context, db DBTX, objectType string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type = ? AND name NOT LIKE 'sqlite_%'
		ORDER BY name`, objectType)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func listMainSQL(ctx context.Context, db DBTX, objectType string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT name, sql FROM sqlite_master
		WHERE type = ? AND name NOT LIKE 'sqlite_%'
		ORDER BY name`, objectType)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		var sqlText sql.NullString
		if err := rows.Scan(&name, &sqlText); err != nil {
			return nil, err
		}
		// Auto-indexes for UNIQUE / PRIMARY KEY have NULL sql; they are implied
		// by the column definitions already compared, and their names are unstable.
		if !sqlText.Valid {
			continue
		}
		normalized := normalizeSQLForCompare(sqlText.String)
		if objectType == "table" {
			normalized = normalizeTableSQLForCompare(name, sqlText.String)
		}
		out = append(out, normalized)
	}
	return out, rows.Err()
}

func readTableColumns(ctx context.Context, db DBTX, table string) ([]Column, error) {
	// Table names come from sqlite_master; quote for identifier safety.
	quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quoted+")")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var cols []Column
	for rows.Next() {
		var cid int
		var col Column
		if err := rows.Scan(&cid, &col.Name, &col.Type, &col.NotNull, &col.Default, &col.PKPosition); err != nil {
			return nil, err
		}
		cols = append(cols, col)
	}
	return cols, rows.Err()
}

// normalizeSQLForCompare tokenizes DDL so equivalent shapes compare equal.
// Punctuation becomes tokens while quoted literals stay intact.
func normalizeSQLForCompare(sqlText string) string {
	var tokens []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			tokens = append(tokens, word.String())
			word.Reset()
		}
	}
	for i := 0; i < len(sqlText); i++ {
		c := sqlText[i]
		switch {
		case c == '-' && i+1 < len(sqlText) && sqlText[i+1] == '-':
			flush()
			for i += 2; i < len(sqlText) && sqlText[i] != '\n'; i++ {
			}
		case c == '/' && i+1 < len(sqlText) && sqlText[i+1] == '*':
			flush()
			for i += 2; i+1 < len(sqlText); i++ {
				if sqlText[i] == '*' && sqlText[i+1] == '/' {
					i++
					break
				}
			}
		case c == '\'':
			flush()
			literal := sqlText[i : i+1]
			for i++; i < len(sqlText); i++ {
				literal += sqlText[i : i+1]
				// '' inside a literal is an escaped quote, not the end of it.
				if sqlText[i] == '\'' {
					if i+1 < len(sqlText) && sqlText[i+1] == '\'' {
						i++
						literal += sqlText[i : i+1]
						continue
					}
					break
				}
			}
			tokens = append(tokens, literal)
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		case c == '(' || c == ')' || c == ',' || c == ';':
			flush()
			tokens = append(tokens, string(c))
		default:
			word.WriteByte(c)
		}
	}
	flush()
	return strings.Join(tokens, " ")
}

func normalizeTableSQLForCompare(name, sqlText string) string {
	trimmed := strings.TrimSpace(sqlText)
	fields := strings.Fields(trimmed)
	if len(fields) < 2 || !strings.EqualFold(fields[0], "CREATE") || !strings.EqualFold(fields[1], "TABLE") {
		return normalizeSQLForCompare(trimmed)
	}
	open := strings.Index(trimmed, "(")
	if open < 0 {
		return normalizeSQLForCompare(trimmed)
	}
	// Compare the authoritative name with the definition tail.
	return strings.Join([]string{"CREATE", "TABLE", name, normalizeSQLForCompare(trimmed[open:])}, " ")
}

func sortedKeys(m map[string][]Column) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func nullString(v sql.NullString) string {
	if !v.Valid {
		return "<NULL>"
	}
	return v.String
}
