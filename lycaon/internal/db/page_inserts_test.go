package db

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPageableInsertsRequireTransaction(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	err = New(sqlDB).InsertCodeScan(t.Context(), codeScanInsertParams("scan-direct"))
	if !errors.Is(err, errPageInsertRequiresTx) {
		t.Fatalf("direct insert error = %v", err)
	}
	assertRowCount(t, sqlDB, "code_scans", "SELECT count(*) FROM code_scans", 0)
}

func TestPageableInsertsCommitEntityAndOrdinal(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	testdbseed.InsertSession(t, sqlDB, "session-1", "project-1")

	tx, err := sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin", err)
	q := New(tx)
	testutil.FailErr(t, "insert scan", q.InsertCodeScan(t.Context(), codeScanInsertParams("scan-1")))
	testutil.FailErr(t, "insert workflow", q.InsertWorkflowRun(t.Context(), workflowRunInsertParams("run-1")))
	testutil.FailErr(t, "commit", tx.Commit())

	assertRowCount(t, sqlDB, "code_scans", "SELECT count(*) FROM code_scans", 1)
	assertRowCount(t, sqlDB, "code_scan_page_ordinals", "SELECT count(*) FROM code_scan_page_ordinals", 1)
	assertRowCount(t, sqlDB, "workflow_runs", "SELECT count(*) FROM workflow_runs", 1)
	assertRowCount(t, sqlDB, "workflow_run_page_ordinals", "SELECT count(*) FROM workflow_run_page_ordinals", 1)
}

func TestPageableInsertRollbackRemovesEntityAndOrdinal(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	tx, err := sqlDB.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin", err)
	testutil.FailErr(t, "insert scan", New(tx).InsertCodeScan(t.Context(), codeScanInsertParams("scan-rollback")))
	testutil.FailErr(t, "rollback", tx.Rollback())

	assertRowCount(t, sqlDB, "code_scans", "SELECT count(*) FROM code_scans", 0)
	assertRowCount(t, sqlDB, "code_scan_page_ordinals", "SELECT count(*) FROM code_scan_page_ordinals", 0)
}

func codeScanInsertParams(id string) InsertCodeScanRowParams {
	return InsertCodeScanRowParams{
		ID: id, CanonicalPath: "/project", CategoriesJson: `["sast"]`,
		Status: "pending", CreatedAt: "2026-01-01T00:00:00Z", ReuseKey: id,
	}
}

func workflowRunInsertParams(id string) InsertWorkflowRunRowParams {
	return InsertWorkflowRunRowParams{
		ID: id, SessionID: "session-1", ProjectID: "project-1",
		WorkflowID: "workflow-1", WorkflowVersion: "1", Status: "running",
		Revision: 1, CurrentPhase: "start", VarsJson: `{}`,
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z",
	}
}

func assertRowCount(t *testing.T, sqlDB DBTX, table, query string, want int) {
	t.Helper()
	var got int
	err := sqlDB.QueryRowContext(t.Context(), query).Scan(&got)
	testutil.FailErr(t, "count "+table, err)
	if got != want {
		t.Fatalf("%s rows = %d, want %d", table, got, want)
	}
}
