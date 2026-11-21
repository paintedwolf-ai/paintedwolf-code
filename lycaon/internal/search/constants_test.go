package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

type schemaLockColumn struct {
	Name    string `json:"name"`
	NotNull bool   `json:"not_null"`
	PK      bool   `json:"pk"`
}

type schemaLock struct {
	Table          string             `json:"table"`
	ProjectIDRole  string             `json:"project_id_role"`
	DefaultScope   string             `json:"default_scope"`
	Columns        []schemaLockColumn `json:"columns"`
	CompositeIndex struct {
		Name    string   `json:"name"`
		Columns []string `json:"columns"`
	} `json:"composite_index"`
	FTSTables []struct {
		Name         string   `json:"name"`
		ContentTable string   `json:"content_table"`
		Columns      []string `json:"columns"`
	} `json:"fts_tables"`
}

func schemaLockPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "schema.lock.json")
}

func loadSchemaLock(t *testing.T) schemaLock {
	t.Helper()
	data, err := os.ReadFile(schemaLockPath(t))
	testutil.FailErr(t, "read schema.lock.json", err)
	var lock schemaLock
	testutil.FailErr(t, "parse schema.lock.json", json.Unmarshal(data, &lock))
	return lock
}

func TestEvidenceIndexSchemaLockMatchesConstants(t *testing.T) {
	lock := loadSchemaLock(t)
	if lock.Table != TableEvidenceIndex {
		t.Fatalf("lock table = %q, want %q", lock.Table, TableEvidenceIndex)
	}
	if lock.DefaultScope != DSLDefaultScope {
		t.Fatalf("default_scope = %q, want %q", lock.DefaultScope, DSLDefaultScope)
	}
	if lock.ProjectIDRole != "attribution_and_origin_ranking_not_isolation_filter" {
		t.Fatalf("project_id_role = %q", lock.ProjectIDRole)
	}
	gotCols := make([]string, len(lock.Columns))
	for i, col := range lock.Columns {
		gotCols[i] = col.Name
	}
	if !reflect.DeepEqual(gotCols, EvidenceIndexColumns()) {
		t.Fatalf("lock columns = %v, constants = %v", gotCols, EvidenceIndexColumns())
	}
	for _, col := range lock.Columns {
		if col.NotNull && !slices.Contains(EvidenceIndexNotNullColumns(), col.Name) {
			t.Fatalf("column %q marked not_null in lock but missing from EvidenceIndexNotNullColumns", col.Name)
		}
	}
	if !reflect.DeepEqual(lock.CompositeIndex.Columns, EvidenceIndexCompositeIndex()) {
		t.Fatalf("composite index = %v, want %v", lock.CompositeIndex.Columns, EvidenceIndexCompositeIndex())
	}
}

func TestFTSTablesLockMatchesConstants(t *testing.T) {
	lock := loadSchemaLock(t)
	if len(lock.FTSTables) != 2 {
		t.Fatalf("expected 2 FTS tables in lock, got %d", len(lock.FTSTables))
	}
	want := map[string][]string{
		TableMessagesFTS: MessagesFTSColumns(),
		TableEvidenceFTS: EvidenceFTSColumns(),
	}
	for _, fts := range lock.FTSTables {
		cols, ok := want[fts.Name]
		if !ok {
			t.Fatalf("unexpected FTS table %q in lock", fts.Name)
		}
		if !reflect.DeepEqual(fts.Columns, cols) {
			t.Fatalf("%s columns = %v, want %v", fts.Name, fts.Columns, cols)
		}
	}
}

// The lock describes the schema a fresh store really has: columns in order, their
// NOT NULL flags, the composite index, and the FTS column sets.
func TestSchemaLockMatchesLiveSchema(t *testing.T) {
	lock := loadSchemaLock(t)
	store := testdbfixture.OpenPath(t, filepath.Join(t.TempDir(), "store.db"))

	type liveColumn struct {
		name    string
		notNull bool
		pk      bool
	}
	columns := func(table string) []liveColumn {
		rows, err := store.QueryContext(t.Context(), "SELECT name, \"notnull\", pk FROM pragma_table_info(?) ORDER BY cid", table)
		testutil.FailErr(t, "table info "+table, err)
		defer func() { _ = rows.Close() }()
		var out []liveColumn
		for rows.Next() {
			var c liveColumn
			testutil.FailErr(t, "scan column", rows.Scan(&c.name, &c.notNull, &c.pk))
			out = append(out, c)
		}
		testutil.FailErr(t, "iterate columns", rows.Err())
		if len(out) == 0 {
			t.Fatalf("live schema has no table %s", table)
		}
		return out
	}

	live := columns(lock.Table)
	if len(live) != len(lock.Columns) {
		t.Fatalf("live %s has %d columns, lock has %d", lock.Table, len(live), len(lock.Columns))
	}
	for i, col := range live {
		want := lock.Columns[i]
		// The lock records a primary key by pk alone, not by not_null.
		if col.name != want.Name || col.pk != want.PK || !col.pk && col.notNull != want.NotNull {
			t.Fatalf("column %d = %+v, lock %+v", i, col, want)
		}
	}

	rows, err := store.QueryContext(t.Context(), "SELECT name FROM pragma_index_info(?) ORDER BY seqno", lock.CompositeIndex.Name)
	testutil.FailErr(t, "index info", err)
	defer func() { _ = rows.Close() }()
	var indexed []string
	for rows.Next() {
		var name string
		testutil.FailErr(t, "scan index column", rows.Scan(&name))
		indexed = append(indexed, name)
	}
	testutil.FailErr(t, "iterate index columns", rows.Err())
	if !slices.Equal(indexed, lock.CompositeIndex.Columns) {
		t.Fatalf("live %s = %v, lock %v", lock.CompositeIndex.Name, indexed, lock.CompositeIndex.Columns)
	}

	for _, fts := range lock.FTSTables {
		var names []string
		for _, col := range columns(fts.Name) {
			names = append(names, col.name)
		}
		if !slices.Equal(names, fts.Columns) {
			t.Fatalf("live %s columns = %v, lock %v", fts.Name, names, fts.Columns)
		}
	}
}

func TestDSLFieldAllowlistIncludesProjectScope(t *testing.T) {
	allow := DSLFieldAllowlist()
	if !slices.Contains(allow, "project") {
		t.Fatal("project must appear in DSLFieldAllowlist for optional scope narrowing")
	}
	for _, field := range []string{"kind", "source", "trust", "verified", "tool", "path", "session", "untrusted", "run", "verdict"} {
		if !slices.Contains(allow, field) {
			t.Fatalf("DSLFieldAllowlist missing %q", field)
		}
	}
}

func TestProjectScopeResolverTokens(t *testing.T) {
	if ProjectScopeCurrent != "current" {
		t.Fatalf("ProjectScopeCurrent = %q", ProjectScopeCurrent)
	}
}

func TestFederationExecutorsAreStoreAndCodeOnly(t *testing.T) {
	if ExecutorStore != "store" || ExecutorCode != "code" {
		t.Fatal("executors must be store and code only")
	}
	if SearchDisplayMaxHits <= 0 || SearchExecutorProbeHits <= SearchDisplayMaxHits {
		t.Fatal("executor hit caps must be positive")
	}
	if ExportMaxHits <= SearchDisplayMaxHits || ExportExecutorProbeHits <= ExportMaxHits {
		t.Fatal("export must have a separate, larger safety ceiling")
	}
}
