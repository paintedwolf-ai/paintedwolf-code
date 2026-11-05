package migrations

import (
	"context"
	"database/sql"
	"math"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/lycaon/lycaon/internal/testutil"
)

type retainedSurveyRecorder struct {
	DBTX
	visits map[string]int
}

func (r retainedSurveyRecorder) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if len(args) == 1 {
		if name, ok := args[0].(string); ok {
			r.visits[name]++
		}
	}
	return r.DBTX.QueryRowContext(ctx, query, args...)
}

func TestRetainedMigrationSurveyDefersUnrelatedAndOldIndexTrees(t *testing.T) {
	source, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "sql.Open failed", err)
	defer source.Close()
	_, err = source.Exec(`CREATE TABLE source_manifest_entries(id INTEGER PRIMARY KEY,path TEXT); INSERT INTO source_manifest_entries VALUES(1,'kept');
 CREATE INDEX old_manifest_path ON source_manifest_entries(path);
 CREATE TABLE unrelated_body(id INTEGER PRIMARY KEY,body BLOB); INSERT INTO unrelated_body VALUES(1,zeroblob(1048576)); CREATE INDEX unrelated_key ON unrelated_body(id)`)
	testutil.FailErr(t, "source.Exec failed", err)
	recorder := retainedSurveyRecorder{DBTX: source, visits: map[string]int{}}
	known, err := retainedInventory(t.Context(), recorder, nil, true)
	testutil.FailErr(t, "retainedInventory failed", err)
	if recorder.visits["unrelated_body"] != 0 || recorder.visits["old_manifest_path"] != 0 || recorder.visits["unrelated_key"] != 0 {
		t.Fatalf("migration scanned old/unrelated trees: %v", recorder.visits)
	}
	if recorder.visits["source_manifest_entries"] != 1 || recorder.visits["sqlite_schema"] != 1 {
		t.Fatalf("route primary inventory missing: %v", recorder.visits)
	}
	_, err = retainedInventory(t.Context(), recorder, known, false)
	testutil.FailErr(t, "retainedInventory failed", err)
	if recorder.visits["old_manifest_path"] != 1 || recorder.visits["unrelated_body"] != 1 {
		t.Fatalf("fallback did not survey reconstruction inputs: %v", recorder.visits)
	}
	if recorder.visits["source_manifest_entries"] != 1 || recorder.visits["sqlite_schema"] != 1 {
		t.Fatalf("fallback rescanned cached primary facts: %v", recorder.visits)
	}
}

func TestRetainedRouteSeparatesCheckpointPayloadAndManifestSorts(t *testing.T) {
	trees := map[string]retainedTree{
		"source_manifest_entries": {cells: 1000000, record: 200, bytes: 300 << 20},
		"checkpoints":             {cells: 1000, record: 4 << 20, bytes: 8 << 20},
	}
	small, err := retainedRouteSortBytes(trees, 4096)
	testutil.FailErr(t, "retainedRouteSortBytes failed", err)
	// A much larger checkpoint payload raises only the bounded33-key class,
	// never every merger window of the million-row manifest class.
	trees["checkpoints"] = retainedTree{cells: 1000, record: 8 << 20, bytes: 16 << 20}
	large, err := retainedRouteSortBytes(trees, 4096)
	testutil.FailErr(t, "retainedRouteSortBytes failed", err)
	if large > small+66*(8<<20) {
		t.Fatalf("checkpoint payload multiplied across manifest mergers: %d -> %d", small, large)
	}
	trees["checkpoints"] = retainedTree{record: math.MaxUint64}
	if _, err := retainedRouteSortBytes(trees, 4096); err == nil {
		t.Fatal("accepted overflowing checkpoint records")
	}
}

func TestRetainedSelectionRejectsPhysicalCountOverflow(t *testing.T) {
	trees := map[string]retainedTree{"source_manifest_chunks": {cells: math.MaxUint64, record: 200}}
	if _, err := retainedRouteSortBytes(trees, 4096); err == nil {
		t.Fatal("accepted overflowing physical selection")
	}
}
