package db

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReviewAssignmentMigrationPreservesBaseline(t *testing.T) {
	source, err := sql.Open("sqlite", "file:review-upgrade?mode=memory&cache=private")
	testutil.FailErr(t, "open source", err)
	defer source.Close()
	schema, err := schemaFS.ReadFile("schema.sql")
	testutil.FailErr(t, "read schema", err)
	_, additions, _ := strings.Cut(string(reviewAssignmentsMigration), "\n")
	additions, _, _ = strings.Cut(additions, "\nALTER TABLE workflow_verdict_operations")
	old := strings.TrimSuffix(string(schema), additions)
	old = strings.Replace(old, "    review_revision INTEGER NOT NULL DEFAULT 0 CHECK (review_revision >= 0),\n", "", 1)
	old = strings.Replace(old, "    updated_at TEXT NOT NULL,\n    evidence_published INTEGER NOT NULL DEFAULT 0 CHECK (evidence_published IN (0, 1))\n", "    updated_at TEXT NOT NULL\n", 1)
	old = strings.Replace(old, " OR (status = 'committed' AND evidence_published = 0)", "", 1)
	if old == string(schema) {
		t.Fatal("migration is not appended to fresh schema")
	}
	_, err = source.ExecContext(t.Context(), old)
	testutil.FailErr(t, "create previous schema", err)
	_, err = source.ExecContext(t.Context(), "PRAGMA user_version=1")
	testutil.FailErr(t, "stamp previous revision", err)
	baseline, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "read previous baseline", err)
	target, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "read current baseline", err)
	step := reviewAssignmentsStep(target)
	if baseline != step.From {
		t.Fatalf("previous baseline is %+v; registered %+v", baseline, step.From)
	}
	tx, err := source.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin migration", err)
	testutil.FailErr(t, "apply migration", step.Apply(t.Context(), tx))
	shape, err := ShapeDigest(t.Context(), tx)
	testutil.FailErr(t, "read migrated shape", err)
	if shape != target.Shape {
		t.Fatalf("migration shape %s, want %s", shape, target.Shape)
	}
	testutil.FailErr(t, "rollback migration", tx.Rollback())
	restored, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect rollback", err)
	if restored != baseline {
		t.Fatal("rollback changed baseline")
	}
}

func TestReleasedCorpusUpgradesWithoutChangingRetainedRows(t *testing.T) {
	original, err := os.ReadFile(filepath.Join("..", "..", "testdata", "upgrade-corpus", "1.0.1", "store.db"))
	testutil.FailErr(t, "read released corpus", err)
	path := filepath.Join(t.TempDir(), "store.db")
	testutil.FailErr(t, "copy released corpus", os.WriteFile(path, original, 0600))
	reader, err := openReader(t.Context(), path)
	testutil.FailErr(t, "inspect copied corpus", err)
	tables := []string{"sessions", "messages", "worker_jobs", "worker_results", "workflow_runs"}
	before := map[string][]string{}
	columns := map[string]string{}
	for _, table := range tables {
		rows, err := reader.QueryContext(t.Context(), "SELECT * FROM "+table)
		testutil.FailErr(t, "read retained rows", err)
		names, err := rows.Columns()
		testutil.FailErr(t, "read retained columns", err)
		for i, name := range names {
			names[i] = `"` + name + `"`
		}
		columns[table] = strings.Join(names, ",")
		before[table] = retainedRows(t, rows)
	}
	testutil.FailErr(t, "close inspection", reader.Close())
	testutil.FailErr(t, "upgrade copied corpus", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "reopen already upgraded corpus", UpgradeStaged(t.Context(), path))
	reader, err = openReader(t.Context(), path)
	testutil.FailErr(t, "inspect upgraded corpus", err)
	defer reader.Close()
	for _, table := range tables {
		rows, err := reader.QueryContext(t.Context(), "SELECT "+columns[table]+" FROM "+table)
		testutil.FailErr(t, "read upgraded rows", err)
		if !reflect.DeepEqual(before[table], retainedRows(t, rows)) {
			t.Errorf("migration changed retained %s rows", table)
		}
	}
	baseline, err := inspectSchema(t.Context(), reader)
	testutil.FailErr(t, "inspect migrated baseline", err)
	target, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "load target baseline", err)
	if baseline != target {
		t.Fatalf("upgraded baseline=%+v, want %+v", baseline, target)
	}
	var applied int
	testutil.FailErr(t, "read migration ledger", reader.QueryRowContext(t.Context(), "SELECT count(*) FROM schema_migrations").Scan(&applied))
	if applied != 1 {
		t.Fatalf("migration applied %d times", applied)
	}
}

func retainedRows(t *testing.T, rows *sql.Rows) []string {
	t.Helper()
	defer rows.Close()
	columns, err := rows.Columns()
	testutil.FailErr(t, "read row columns", err)
	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		refs := make([]any, len(columns))
		for i := range refs {
			refs[i] = &values[i]
		}
		testutil.FailErr(t, "read retained row", rows.Scan(refs...))
		raw, err := json.Marshal(values)
		testutil.FailErr(t, "encode retained row", err)
		out = append(out, string(raw))
	}
	testutil.FailErr(t, "finish retained rows", rows.Err())
	slices.Sort(out)
	return out
}
