package db

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRetainedStrategiesPreserveTheSameLogicalStore(t *testing.T) {
	source, _ := completeRetainedFixture(t)
	sequenceTable, sequence := seedStrategySequence(t, source)
	reader, err := openReader(t.Context(), source)
	testutil.FailErr(t, "open shared strategy source", err)
	plan, err := PlanUpgrade(t.Context(), reader)
	testutil.FailErr(t, "plan shared source", err)
	plan, err = plan.PrepareRetainedStorage(t.Context(), reader)
	testutil.FailErr(t, "measure shared source", err)
	testutil.FailErr(t, "close shared source", reader.Close())
	testutil.FailErr(t, "seal shared source", sealRebuiltStore(t.Context(), source))
	before, err := os.ReadFile(source)
	testutil.FailErr(t, "retain original bytes", err)
	registry, err := schemaRegistry(t.Context())
	testutil.FailErr(t, "load common migration registry", err)
	whole := filepath.Join(t.TempDir(), "whole.db")
	prepared, used, err := copyWholeRetainedStore(t.Context(), source, whole, plan)
	testutil.FailErr(t, "prepare whole-file strategy", err)
	if !used || !prepared.wholeFile || prepared.preparedIdentity == "" {
		t.Fatal("complete source did not exercise the whole-file strategy")
	}
	rows := filepath.Join(t.TempDir(), "rows.db")
	testutil.FailErr(t, "force row reconstruction from same source", copyRetainedRows(t.Context(), source, rows, plan))
	for _, image := range []struct {
		path   string
		shared bool
	}{{whole, prepared.sharedOriginal}, {rows, false}} {
		testutil.FailErr(t, "execute identical registered route", executeRetainedRoute(t.Context(), image.path, plan, nil, registry, true, image.shared))
		testutil.FailErr(t, "seal migrated strategy", sealRebuiltStore(t.Context(), image.path))
		database, err := openReader(t.Context(), image.path)
		testutil.FailErr(t, "open migrated strategy", err)
		testutil.FailErr(t, "validate all strategy pages and foreign keys", auditSnapshot(t.Context(), database, nil))
		var got int64
		err = database.QueryRowContext(t.Context(), "SELECT seq FROM sqlite_sequence WHERE name=?", sequenceTable).Scan(&got)
		testutil.FailErr(t, "read retained sequence ceiling", err)
		if got != sequence {
			t.Fatalf("strategy sequence=%d, want %d", got, sequence)
		}
		testutil.FailErr(t, "close migrated strategy", database.Close())
	}
	left := strategyStoreImage(t, whole)
	right := strategyStoreImage(t, rows)
	if len(left) != len(right) {
		t.Fatalf("strategy objects differ: whole=%d rows=%d", len(left), len(right))
	}
	for name, want := range left {
		got, exists := right[name]
		if !exists || !reflect.DeepEqual(got, want) {
			t.Fatalf("strategy logical state differs for %s (present=%v, whole rows=%d, reconstructed rows=%d)", name, exists, len(want.rows), len(got.rows))
		}
	}
	after, err := os.ReadFile(source)
	testutil.FailErr(t, "read original after both strategies", err)
	if !bytes.Equal(before, after) {
		t.Fatal("a private strategy changed the shared sealed source")
	}
}

func seedStrategySequence(t *testing.T, path string) (string, int64) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open sequence seed", err)
	defer func() { _ = database.Close() }()
	var table string
	err = database.QueryRowContext(t.Context(), `SELECT name FROM sqlite_schema WHERE type='table' AND instr(upper(sql),'AUTOINCREMENT')>0 ORDER BY name LIMIT 1`).Scan(&table)
	testutil.FailErr(t, "select durable autoincrement table", err)
	var maximum int64
	err = database.QueryRowContext(t.Context(), "SELECT COALESCE(MAX(rowid),0) FROM "+rebuildIdentifier(table)).Scan(&maximum)
	testutil.FailErr(t, "read allocated rowid ceiling", err)
	want := maximum + 1000
	result, err := database.ExecContext(t.Context(), "UPDATE sqlite_sequence SET seq=? WHERE name=?", want, table)
	testutil.FailErr(t, "retain sequence beyond live rows", err)
	updated, err := result.RowsAffected()
	testutil.FailErr(t, "inspect sequence update", err)
	if updated == 0 {
		_, err = database.ExecContext(t.Context(), "INSERT INTO sqlite_sequence(name,seq) VALUES(?,?)", table, want)
		testutil.FailErr(t, "seed empty table sequence", err)
	}
	return table, want
}

type strategyRows struct {
	columns []string
	rows    []string
}

func strategyStoreImage(t *testing.T, path string) map[string]strategyRows {
	t.Helper()
	database, err := openReader(t.Context(), path)
	testutil.FailErr(t, "open logical strategy image", err)
	defer func() { _ = database.Close() }()
	out := map[string]strategyRows{
		"schema": strategyQueryRows(t, database, false, `SELECT type,name,tbl_name,sql FROM sqlite_schema WHERE sql IS NOT NULL`),
	}
	tables, err := database.QueryContext(t.Context(), `SELECT name,type,wr FROM pragma_table_list WHERE schema='main' AND name!='sqlite_schema' ORDER BY name`)
	testutil.FailErr(t, "enumerate logical and shadow tables", err)
	var names []string
	rowIDs := map[string]bool{}
	shadows := 0
	for tables.Next() {
		var name, kind string
		var withoutRowID bool
		testutil.FailErr(t, "read strategy table", tables.Scan(&name, &kind, &withoutRowID))
		names = append(names, name)
		switch kind {
		case "table", "shadow", "virtual":
			rowIDs[name] = !withoutRowID
		case "view":
			rowIDs[name] = false
		default:
			t.Fatalf("unknown strategy object kind %q for %s", kind, name)
		}
		if kind == "shadow" {
			shadows++
		}
	}
	testutil.FailErr(t, "finish strategy table inventory", tables.Err())
	testutil.FailErr(t, "close strategy table inventory", tables.Close())
	if shadows == 0 {
		t.Fatal("equivalence fixture omitted FTS shadow state")
	}
	for _, name := range names {
		projection := "*"
		if rowIDs[name] {
			projection = "rowid,*"
		}
		out["table/"+name] = strategyQueryRows(t, database, name == "schema_migrations", "SELECT "+projection+" FROM "+rebuildIdentifier(name))
		out["indexes/"+name] = strategyQueryRows(t, database, false, `SELECT name,"unique",origin,partial FROM pragma_index_list(?)`, name)
		out["foreign keys/"+name] = strategyQueryRows(t, database, false, `SELECT * FROM pragma_foreign_key_list(?)`, name)
	}
	indexes, err := database.QueryContext(t.Context(), "SELECT name FROM sqlite_schema WHERE type='index'")
	testutil.FailErr(t, "enumerate strategy indexes", err)
	var indexNames []string
	for indexes.Next() {
		var name string
		testutil.FailErr(t, "read strategy index", indexes.Scan(&name))
		indexNames = append(indexNames, name)
	}
	testutil.FailErr(t, "finish strategy indexes", indexes.Err())
	testutil.FailErr(t, "close strategy indexes", indexes.Close())
	for _, name := range indexNames {
		out["index keys/"+name] = strategyQueryRows(t, database, false, "SELECT * FROM pragma_index_xinfo(?)", name)
	}
	return out
}

func strategyQueryRows(t *testing.T, database *sql.DB, migrationTimes bool, query string, args ...any) strategyRows {
	t.Helper()
	identity := fmt.Sprintf("%s (args=%v)", query, args)
	rows, err := database.QueryContext(t.Context(), query, args...)
	testutil.FailErr(t, "read strategy state: "+identity, err)
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	testutil.FailErr(t, "read strategy columns: "+identity, err)
	out := strategyRows{columns: columns, rows: []string{}}
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for i := range destinations {
			destinations[i] = &values[i]
		}
		testutil.FailErr(t, "read strategy values: "+identity, rows.Scan(destinations...))
		encoded := make([]struct {
			Kind  string
			Value any
		}, len(values))
		for i, value := range values {
			if migrationTimes && columns[i] == "applied_at" {
				value = "<migration time>"
			}
			encoded[i].Kind, encoded[i].Value = fmt.Sprintf("%T", value), value
		}
		raw, err := json.Marshal(encoded)
		testutil.FailErr(t, "encode typed logical row", err)
		out.rows = append(out.rows, string(raw))
	}
	testutil.FailErr(t, "finish logical strategy rows: "+identity, rows.Err())
	sort.Strings(out.rows)
	return out
}
