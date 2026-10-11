package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type schemaAuditIndex struct {
	table, name, definition string
	keys                    []string
}

func redundantExplicitIndexes(t *testing.T, store DBTX) []string {
	t.Helper()
	rows, err := store.QueryContext(t.Context(), `
		SELECT s.tbl_name, s.name, s.sql
		FROM sqlite_schema s, pragma_index_list(s.tbl_name) il
		WHERE s.type = 'index' AND il.name = s.name
		  AND il.partial = 0 AND il.origin = 'c' AND il."unique" = 0
		ORDER BY s.tbl_name, s.name`)
	testutil.FailErr(t, "list explicit indexes", err)
	var indexes []schemaAuditIndex
	for rows.Next() {
		var index schemaAuditIndex
		testutil.FailErr(t, "read explicit index", rows.Scan(&index.table, &index.name, &index.definition))
		indexes = append(indexes, index)
	}
	testutil.FailErr(t, "iterate explicit indexes", rows.Err())
	testutil.FailErr(t, "close explicit indexes", rows.Close())
	for i := range indexes {
		index := &indexes[i]
		terms, err := sqliteIndexTerms(index.definition)
		testutil.FailErr(t, "read terms for "+index.name, err)
		keys, err := store.QueryContext(t.Context(), `
			SELECT seqno, cid, name, desc, coll
			FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno`, index.name)
		testutil.FailErr(t, "read index metadata", err)
		for keys.Next() {
			var seqno, cid, descending int
			var name sql.NullString
			var collation string
			testutil.FailErr(t, "read index key", keys.Scan(&seqno, &cid, &name, &descending, &collation))
			identity := fmt.Sprintf("column:%d", cid)
			if cid == -2 {
				if seqno >= len(terms) {
					t.Fatalf("index %s key %d has no expression term", index.name, seqno)
				}
				identity = "expression:" + terms[seqno]
			}
			index.keys = append(index.keys, fmt.Sprintf("%s:%d:%s", identity, descending, strings.ToLower(collation)))
		}
		testutil.FailErr(t, "iterate index keys", keys.Err())
		testutil.FailErr(t, "close index keys", keys.Close())
	}
	var redundant []string
	for _, short := range indexes {
		for _, long := range indexes {
			if short.table == long.table && short.name != long.name && len(short.keys) <= len(long.keys) && slices.Equal(short.keys, long.keys[:len(short.keys)]) {
				redundant = append(redundant, short.table+"."+short.name+" covered by "+long.name)
			}
		}
	}
	sort.Strings(redundant)
	return redundant
}

func TestNoRedundantExplicitIndexes(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = store.Close() })
	if redundant := redundantExplicitIndexes(t, store); len(redundant) > 0 {
		t.Fatalf("redundant indexes: %s", strings.Join(redundant, "; "))
	}
}

func TestIndexRedundancyUsesExpressionIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, left, right string
		covered           []string
	}{
		{"different JSON columns", "(project_id, json_extract(undo_json, '$.recovery_id'))", "(project_id, json_extract(redo_json, '$.recovery_id'))", nil},
		{"same JSON expression", "(project_id, json_extract(undo_json, '$.recovery_id'))", "(project_id, JSON_EXTRACT ( undo_json , '$.recovery_id' ) ASC)", []string{"left", "right"}},
		{"column prefix", "(project_id)", "(project_id, json_extract(undo_json, '$.recovery_id'))", []string{"left"}},
		{"expression prefix", "(project_id, json_extract(undo_json, '$.recovery_id'))", "(project_id, json_extract(undo_json, '$.recovery_id'), marker)", []string{"left"}},
		{"different direction", "(project_id, json_extract(undo_json, '$.recovery_id') ASC)", "(project_id, json_extract(undo_json, '$.recovery_id') DESC)", nil},
		{"different collation", "(project_id, lower(marker) COLLATE NOCASE)", "(project_id, lower(marker) COLLATE BINARY)", nil},
		{"quoted punctuation", "(project_id, instr(marker, ',('))", "(project_id, INSTR(marker, ',(') COLLATE BINARY)", []string{"left", "right"}},
		{"comments", "(project_id, lower(marker))", "(project_id /* , ) */, lower( /* ( */ marker))", []string{"left", "right"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := Open(filepath.Join(t.TempDir(), "store.db"))
			testutil.FailErr(t, "open", err)
			t.Cleanup(func() { _ = store.Close() })
			_, err = store.ExecContext(t.Context(), `CREATE TABLE index_fixture(project_id TEXT, undo_json TEXT, redo_json TEXT, marker TEXT) STRICT`)
			testutil.FailErr(t, "create index fixture", err)
			for _, index := range []struct{ name, terms string }{{"left", tc.left}, {"right", tc.right}} {
				_, err = store.ExecContext(t.Context(), `CREATE INDEX "`+index.name+`" ON index_fixture`+index.terms)
				testutil.FailErr(t, "create fixture index", err)
			}
			var want []string
			for _, name := range tc.covered {
				other := "right"
				if name == "right" {
					other = "left"
				}
				want = append(want, "index_fixture."+name+" covered by "+other)
			}
			if got := redundantExplicitIndexes(t, store); !slices.Equal(got, want) {
				t.Fatalf("redundant indexes = %v; want %v", got, want)
			}
		})
	}
}
