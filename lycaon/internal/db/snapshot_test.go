package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	_ "modernc.org/sqlite"
)

func TestCreateSnapshotIncludesCommittedWALState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "store.db")
	live, err := Open(dbPath)
	testutil.FailErr(t, "open live store", err)
	defer live.Close()
	_, err = live.ExecContext(t.Context(), `PRAGMA wal_autocheckpoint = 0`)
	testutil.FailErr(t, "disable WAL autocheckpoint", err)
	_, err = live.ExecContext(t.Context(), `CREATE TABLE snapshot_probe (value TEXT NOT NULL)`)
	testutil.FailErr(t, "create probe table", err)
	_, err = live.ExecContext(t.Context(), `INSERT INTO snapshot_probe(value) VALUES ('latest')`)
	testutil.FailErr(t, "insert latest WAL row", err)

	snapshotPath := filepath.Join(t.TempDir(), "snapshot.db")
	testutil.FailErr(t, "create online snapshot", CreateSnapshot(t.Context(), live, snapshotPath))

	snapshot, err := sql.Open("sqlite", snapshotPath)
	testutil.FailErr(t, "open snapshot", err)
	defer snapshot.Close()
	var value string
	err = snapshot.QueryRowContext(t.Context(), `SELECT value FROM snapshot_probe`).Scan(&value)
	testutil.FailErr(t, "read snapshot row", err)
	if value != "latest" {
		t.Fatalf("snapshot value = %q, want latest", value)
	}
}
