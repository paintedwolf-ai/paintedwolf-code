package search

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type schemaLockColumn struct {
	Name    string `json:"name"`
	NotNull bool   `json:"not_null"`
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
